package repodojo

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ObjectKind int

const (
	ObjectKindBlob ObjectKind = iota
	ObjectKindTree
	ObjectKindCommit
	ObjectKindTag
)

func (k ObjectKind) Name() string {
	switch k {
	case ObjectKindBlob:
		return "blob"
	case ObjectKindTree:
		return "tree"
	case ObjectKindCommit:
		return "commit"
	case ObjectKindTag:
		return "tag"
	}
	return ""
}

func ObjectKindFromName(name string) (ObjectKind, error) {
	switch name {
	case "blob":
		return ObjectKindBlob, nil
	case "tree":
		return ObjectKindTree, nil
	case "commit":
		return ObjectKindCommit, nil
	case "tag":
		return ObjectKindTag, nil
	}
	return 0, fmt.Errorf("invalid object kind: %s", name)
}

type ObjectHeader struct {
	Kind ObjectKind
	Size uint64
}

// writeObject writes an object to loose storage by streaming from a reader.
// The header is prepended and the content is hashed and zlib-compressed.
func (repo *Repo) writeObject(header ObjectHeader, reader io.Reader) ([]byte, error) {
	headerStr := fmt.Sprintf("%s %d\x00", header.Kind.Name(), header.Size)

	tempFile, err := os.CreateTemp(repo.repoDir, "object.temp.*")
	if err != nil {
		return nil, err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	defer tempFile.Close()

	hasher := repo.opts.Hash.NewHasher()
	hasher.Write([]byte(headerStr))
	if _, err := tempFile.Write([]byte(headerStr)); err != nil {
		return nil, err
	}

	w := io.MultiWriter(tempFile, hasher)
	if _, err := io.Copy(w, reader); err != nil {
		return nil, err
	}

	oidBytes := hasher.Sum(nil)
	oidHex := hex.EncodeToString(oidBytes)

	objDir := filepath.Join(repo.repoDir, "objects", oidHex[:2])
	objPath := filepath.Join(objDir, oidHex[2:])
	if _, err := os.Stat(objPath); err == nil {
		return oidBytes, nil
	}

	if err := os.MkdirAll(objDir, 0755); err != nil {
		return nil, err
	}

	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	lock, err := NewLockFile(objDir, oidHex[2:])
	if err != nil {
		return nil, err
	}
	defer lock.Close()

	zlibW := zlib.NewWriter(lock.File)
	if _, err := io.Copy(zlibW, tempFile); err != nil {
		return nil, err
	}
	if err := zlibW.Close(); err != nil {
		return nil, err
	}

	lock.Success = true
	return oidBytes, nil
}

// readLooseObject reads and decompresses a loose git object.
func (repo *Repo) readLooseObject(oidHex string) ([]byte, error) {
	objPath := filepath.Join(repo.repoDir, "objects", oidHex[:2], oidHex[2:])
	file, err := os.Open(objPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	r, err := zlib.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	return io.ReadAll(r)
}

// parseObjectHeader parses a git object header from decompressed data.
func parseObjectHeader(data []byte) (ObjectHeader, int, error) {
	spaceIdx := bytes.IndexByte(data, ' ')
	if spaceIdx < 0 {
		return ObjectHeader{}, 0, errors.New("invalid object header: no space")
	}

	kind, err := ObjectKindFromName(string(data[:spaceIdx]))
	if err != nil {
		return ObjectHeader{}, 0, err
	}

	nullIdx := bytes.IndexByte(data[spaceIdx+1:], 0)
	if nullIdx < 0 {
		return ObjectHeader{}, 0, errors.New("invalid object header: no null")
	}
	nullIdx += spaceIdx + 1

	var size uint64
	_, err = fmt.Sscanf(string(data[spaceIdx+1:nullIdx]), "%d", &size)
	if err != nil {
		return ObjectHeader{}, 0, fmt.Errorf("invalid object size: %w", err)
	}

	return ObjectHeader{Kind: kind, Size: size}, nullIdx + 1, nil
}

// readObjectKind reads a loose object and returns just its kind.
func (repo *Repo) readObjectKind(oidHex string) (ObjectKind, error) {
	data, err := repo.readLooseObject(oidHex)
	if err != nil {
		return 0, err
	}
	header, _, err := parseObjectHeader(data)
	if err != nil {
		return 0, err
	}
	return header.Kind, nil
}

// readCommitTree reads a commit object and returns its tree hash hex string.
func (repo *Repo) readCommitTree(oidHex string) (string, error) {
	data, err := repo.readLooseObject(oidHex)
	if err != nil {
		return "", err
	}
	_, contentStart, err := parseObjectHeader(data)
	if err != nil {
		return "", err
	}
	content := data[contentStart:]

	if !bytes.HasPrefix(content, []byte("tree ")) {
		return "", errors.New("invalid commit: missing tree")
	}
	hexLen := repo.opts.Hash.HexLen()
	if len(content) < 5+hexLen {
		return "", errors.New("invalid commit: tree hash too short")
	}
	treeHash := string(content[5 : 5+hexLen])
	return treeHash, nil
}

// treeBuilder accumulates entries for a git tree object.
type treeBuilder struct {
	entries []treeBuilderEntry
}

type treeBuilderEntry struct {
	sortKey string
	data    []byte
}

func newTreeBuilder() *treeBuilder {
	return &treeBuilder{}
}

func (t *treeBuilder) addBlobEntry(mode Mode, name string, oidBytes []byte) {
	header := fmt.Sprintf("%s %s\x00", mode.String(), name)
	data := make([]byte, len(header)+len(oidBytes))
	copy(data, header)
	copy(data[len(header):], oidBytes)
	t.entries = append(t.entries, treeBuilderEntry{
		sortKey: name,
		data:    data,
	})
}

func (t *treeBuilder) addTreeEntry(name string, oidBytes []byte) {
	header := fmt.Sprintf("40000 %s\x00", name)
	data := make([]byte, len(header)+len(oidBytes))
	copy(data, header)
	copy(data[len(header):], oidBytes)
	t.entries = append(t.entries, treeBuilderEntry{
		sortKey: name + "/",
		data:    data,
	})
}

func (t *treeBuilder) addIndexEntries(repo *Repo, idx *Index, prefix string, childNames []string) error {
	for _, name := range childNames {
		var path string
		if prefix == "" {
			path = name
		} else {
			path = JoinPath([]string{prefix, name})
		}

		if entries, ok := idx.entries[path]; ok {
			entry := entries[0]
			if entry != nil {
				t.addBlobEntry(entry.mode, name, entry.oid)
			}
		} else if children, ok := idx.dirToChildren[path]; ok {
			subtree := newTreeBuilder()

			var childNamesList []string
			for k := range children {
				childNamesList = append(childNamesList, k)
			}

			if err := subtree.addIndexEntries(repo, idx, path, childNamesList); err != nil {
				return err
			}

			subtreeOID, err := repo.writeTree(subtree)
			if err != nil {
				return err
			}
			t.addTreeEntry(name, subtreeOID)
		} else {
			return fmt.Errorf("object entry not found: %s", path)
		}
	}

	return nil
}

func (repo *Repo) writeTree(tree *treeBuilder) ([]byte, error) {
	sort.Slice(tree.entries, func(i, j int) bool {
		return tree.entries[i].sortKey < tree.entries[j].sortKey
	})

	var content bytes.Buffer
	for _, e := range tree.entries {
		content.Write(e.data)
	}

	return repo.writeObject(
		ObjectHeader{Kind: ObjectKindTree, Size: uint64(content.Len())},
		&content,
	)
}

func (repo *Repo) writeBlob(content []byte) ([]byte, error) {
	return repo.writeObject(
		ObjectHeader{Kind: ObjectKindBlob, Size: uint64(len(content))},
		bytes.NewReader(content),
	)
}

func (repo *Repo) writeBlobFromReader(reader io.Reader, size uint64) ([]byte, error) {
	return repo.writeObject(
		ObjectHeader{Kind: ObjectKindBlob, Size: size},
		reader,
	)
}

// CommitMetadata holds metadata for creating a commit.
type CommitMetadata struct {
	Author     string
	Committer  string
	Message    string
	ParentOIDs []string // hex strings; nil means "use HEAD"
	AllowEmpty bool
}

// signContent signs content using ssh-keygen and returns the signature lines.
func (repo *Repo) signContent(lines []string, signingKey string) ([]string, error) {
	content := strings.Join(lines, "\n")

	contentFileName := "xit_signing_buffer"
	contentFilePath := filepath.Join(repo.workPath, ".git", contentFileName)
	if err := os.WriteFile(contentFilePath, []byte(content), 0644); err != nil {
		return nil, err
	}
	defer os.Remove(contentFilePath)

	cmd := exec.Command("ssh-keygen", "-Y", "sign", "-n", "git", "-f", signingKey, contentFilePath)
	if !repo.opts.IsTest {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("object signing failed: %w", err)
	}

	sigFileName := contentFileName + ".sig"
	sigFilePath := filepath.Join(repo.repoDir, sigFileName)
	sigData, err := os.ReadFile(sigFilePath)
	if err != nil {
		return nil, err
	}
	defer os.Remove(sigFilePath)

	sigContent := strings.TrimRight(string(sigData), "\n")
	return strings.Split(sigContent, "\n"), nil
}

func (repo *Repo) checkForUnfinishedMerge() error {
	mergeHeadNames := []string{"MERGE_HEAD", "CHERRY_PICK_HEAD"}
	for _, name := range mergeHeadNames {
		ref := RefOrOid{IsRef: true, Ref: Ref{Kind: RefNone, Name: name}}
		oid, err := repo.readRefRecur(ref)
		if err != nil && !errors.Is(err, ErrRefNotFound) {
			return err
		}
		if oid != "" {
			return errors.New("unfinished merge in progress")
		}
	}
	return nil
}

// writeCommit creates a new commit object and updates HEAD.
func (repo *Repo) writeCommit(metadata CommitMetadata) (string, error) {
	parentOIDs := metadata.ParentOIDs
	if parentOIDs == nil {
		headOID, err := repo.ReadHeadRecurMaybe()
		if err != nil {
			return "", err
		}
		if headOID != "" {
			parentOIDs = []string{headOID}
		} else {
			parentOIDs = []string{}
		}
	}

	if err := repo.checkForUnfinishedMerge(); err != nil {
		return "", err
	}

	idx, err := repo.readIndex()
	if err != nil {
		return "", err
	}

	var rootChildNames []string
	for k := range idx.rootChildren {
		rootChildNames = append(rootChildNames, k)
	}

	tree := newTreeBuilder()
	if err := tree.addIndexEntries(repo, idx, "", rootChildNames); err != nil {
		return "", err
	}
	treeOIDBytes, err := repo.writeTree(tree)
	if err != nil {
		return "", err
	}
	treeOIDHex := hex.EncodeToString(treeOIDBytes)

	if !metadata.AllowEmpty {
		if len(parentOIDs) == 0 {
			if len(rootChildNames) == 0 {
				return "", errors.New("empty commit")
			}
		} else if len(parentOIDs) == 1 {
			parentTree, err := repo.readCommitTree(parentOIDs[0])
			if err != nil {
				return "", err
			}
			if parentTree == treeOIDHex {
				return "", errors.New("empty commit")
			}
		}
	}

	config, err := repo.loadConfig()
	if err != nil {
		return "", err
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("tree %s", treeOIDHex))
	for _, parent := range parentOIDs {
		lines = append(lines, fmt.Sprintf("parent %s", parent))
	}

	var ts uint64
	if !repo.opts.IsTest {
		ts = uint64(time.Now().Unix())
	}

	author := metadata.Author
	if author == "" {
		if repo.opts.IsTest {
			author = "radar <radar@roark>"
		} else {
			userSection := config.GetSection("user")
			if userSection == nil {
				return "", errors.New("user config not found")
			}
			name, ok1 := userSection["name"]
			email, ok2 := userSection["email"]
			if !ok1 || !ok2 {
				return "", errors.New("user config not found")
			}
			author = fmt.Sprintf("%s <%s>", name, email)
		}
	}
	lines = append(lines, fmt.Sprintf("author %s %d +0000", author, ts))

	committer := metadata.Committer
	if committer == "" {
		committer = author
	}
	lines = append(lines, fmt.Sprintf("committer %s %d +0000", committer, ts))

	lines = append(lines, fmt.Sprintf("\n%s", metadata.Message))

	userSection := config.GetSection("user")
	if userSection != nil {
		if signingKey, ok := userSection["signingkey"]; ok {
			sigLines, err := repo.signContent(lines, signingKey)
			if err != nil {
				return "", err
			}

			var headerLines []string
			for i, line := range sigLines {
				if i == 0 {
					headerLines = append(headerLines, fmt.Sprintf("gpgsig %s", line))
				} else {
					headerLines = append(headerLines, fmt.Sprintf(" %s", line))
				}
			}

			msg := lines[len(lines)-1]
			lines = lines[:len(lines)-1]
			lines = append(lines, headerLines...)
			lines = append(lines, msg)
		}
	}

	commitContent := strings.Join(lines, "\n")

	oidBytes, err := repo.writeObject(
		ObjectHeader{Kind: ObjectKindCommit, Size: uint64(len(commitContent))},
		strings.NewReader(commitContent),
	)
	if err != nil {
		return "", err
	}
	oidHex := hex.EncodeToString(oidBytes)

	if err := repo.writeRefRecur("HEAD", oidHex); err != nil {
		return "", err
	}

	return oidHex, nil
}

// writeTag creates a new tag object. Returns the hex OID.
func (repo *Repo) writeTag(input AddTagInput, targetOID string) (string, error) {
	targetKind, err := repo.readObjectKind(targetOID)
	if err != nil {
		return "", err
	}

	config, err := repo.loadConfig()
	if err != nil {
		return "", err
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("object %s", targetOID))
	lines = append(lines, fmt.Sprintf("type %s", targetKind.Name()))
	lines = append(lines, fmt.Sprintf("tag %s", input.Name))

	var ts uint64
	if !repo.opts.IsTest {
		ts = uint64(time.Now().Unix())
	}

	tagger := input.Tagger
	if tagger == "" {
		if repo.opts.IsTest {
			tagger = "radar <radar@roark>"
		} else {
			userSection := config.GetSection("user")
			if userSection == nil {
				return "", errors.New("user config not found")
			}
			name, ok1 := userSection["name"]
			email, ok2 := userSection["email"]
			if !ok1 || !ok2 {
				return "", errors.New("user config not found")
			}
			tagger = fmt.Sprintf("%s <%s>", name, email)
		}
	}
	lines = append(lines, fmt.Sprintf("tagger %s %d +0000", tagger, ts))

	msg := input.Message
	lines = append(lines, fmt.Sprintf("\n%s", msg))

	userSection := config.GetSection("user")
	if userSection != nil {
		if signingKey, ok := userSection["signingkey"]; ok {
			sigLines, err := repo.signContent(lines, signingKey)
			if err != nil {
				return "", err
			}
			lines = append(lines, sigLines...)
		}
	}

	tagContent := strings.Join(lines, "\n")

	oidBytes, err := repo.writeObject(
		ObjectHeader{Kind: ObjectKindTag, Size: uint64(len(tagContent))},
		strings.NewReader(tagContent),
	)
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(oidBytes), nil
}

// ---------------------------------------------------------------------------
// ObjectReader – streaming reader for a single git object
// ---------------------------------------------------------------------------

// ObjectReader reads an object from loose storage or from a pack file.
type ObjectReader interface {
	Close()
	Header() ObjectHeader
	Reset() error
	Read(p []byte) (int, error)
	SkipBytes(n uint64) error
	Position() uint64
}

func (repo *Repo) NewObjectReader(oidHex string) (ObjectReader, error) {
	return newObjectReader(repo, oidHex)
}

// ---------------------------------------------------------------------------
// Parsed object content types
// ---------------------------------------------------------------------------

type CommitContent struct {
	Tree       string   // hex OID of tree
	ParentOIDs []string // hex OIDs of parents
	Author     string
	Committer  string
	Message    string
}

type TreeContentEntry struct {
	Name string
	Mode Mode
	OID  []byte // raw bytes
}

type TreeContent struct {
	Entries []TreeContentEntry
}

type TagContent struct {
	Target  string // hex OID
	Kind    ObjectKind
	Name    string
	Tagger  string
	Message string
}

// ---------------------------------------------------------------------------
// Object – a fully or partially parsed git object
// ---------------------------------------------------------------------------

type Object struct {
	OID  string // hex
	Kind ObjectKind
	Size uint64

	// Full-mode parsed content (nil for raw mode)
	Commit *CommitContent
	Tree   *TreeContent
	Tag    *TagContent

	reader ObjectReader
}

func (repo *Repo) NewObject(oidHex string, full bool) (*Object, error) {
	rdr, err := repo.NewObjectReader(oidHex)
	if err != nil {
		return nil, err
	}
	header := rdr.Header()

	obj := &Object{
		OID:    oidHex,
		Kind:   header.Kind,
		Size:   header.Size,
		reader: rdr,
	}

	if full {
		if err := obj.parseContent(repo.opts.Hash); err != nil {
			rdr.Close()
			return nil, err
		}
	}

	return obj, nil
}

func (o *Object) Close() {
	if o.reader != nil {
		o.reader.Close()
	}
}

func (o *Object) parseContent(hashKind HashKind) error {
	switch o.Kind {
	case ObjectKindBlob:
		return nil
	case ObjectKindTree:
		return o.parseTree(hashKind)
	case ObjectKindCommit:
		return o.parseCommit()
	case ObjectKindTag:
		return o.parseTag()
	}
	return nil
}

func (o *Object) parseTree(hashKind HashKind) error {
	data, err := io.ReadAll(o.reader)
	if err != nil {
		return err
	}
	byteLen := hashKind.ByteLen()
	var entries []TreeContentEntry
	pos := 0
	for pos < len(data) {
		spIdx := bytes.IndexByte(data[pos:], ' ')
		if spIdx < 0 {
			return errors.New("invalid tree entry: no space")
		}
		modeStr := string(data[pos : pos+spIdx])
		pos += spIdx + 1

		nullIdx := bytes.IndexByte(data[pos:], 0)
		if nullIdx < 0 {
			return errors.New("invalid tree entry: no null")
		}
		name := string(data[pos : pos+nullIdx])
		pos += nullIdx + 1

		if pos+byteLen > len(data) {
			return errors.New("invalid tree entry: truncated OID")
		}
		oid := make([]byte, byteLen)
		copy(oid, data[pos:pos+byteLen])
		pos += byteLen

		modeVal, err := strconv.ParseUint(modeStr, 8, 32)
		if err != nil {
			return fmt.Errorf("invalid tree entry mode: %w", err)
		}

		entries = append(entries, TreeContentEntry{
			Name: name,
			Mode: Mode(modeVal),
			OID:  oid,
		})
	}
	o.Tree = &TreeContent{Entries: entries}
	return nil
}

func (o *Object) parseCommit() error {
	data, err := io.ReadAll(o.reader)
	if err != nil {
		return err
	}
	content := string(data)
	cc := &CommitContent{}

	parts := strings.SplitN(content, "\n\n", 2)
	headerSection := parts[0]
	if len(parts) > 1 {
		cc.Message = strings.TrimRight(parts[1], "\n")
	}

	lines := strings.Split(headerSection, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "tree ") {
			cc.Tree = line[5:]
		} else if strings.HasPrefix(line, "parent ") {
			cc.ParentOIDs = append(cc.ParentOIDs, line[7:])
		} else if strings.HasPrefix(line, "author ") {
			cc.Author = line[7:]
		} else if strings.HasPrefix(line, "committer ") {
			cc.Committer = line[10:]
		}
	}

	o.Commit = cc
	return nil
}

func (o *Object) parseTag() error {
	data, err := io.ReadAll(o.reader)
	if err != nil {
		return err
	}
	content := string(data)
	tc := &TagContent{}

	parts := strings.SplitN(content, "\n\n", 2)
	headerSection := parts[0]
	if len(parts) > 1 {
		tc.Message = parts[1]
	}

	lines := strings.Split(headerSection, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "object ") {
			tc.Target = line[7:]
		} else if strings.HasPrefix(line, "type ") {
			k, err := ObjectKindFromName(line[5:])
			if err == nil {
				tc.Kind = k
			}
		} else if strings.HasPrefix(line, "tag ") {
			tc.Name = line[4:]
		} else if strings.HasPrefix(line, "tagger ") {
			tc.Tagger = line[7:]
		}
	}

	o.Tag = tc
	return nil
}

// ---------------------------------------------------------------------------
// ObjectIterator – graph-walking iterator over reachable objects
// ---------------------------------------------------------------------------

type ObjectIterKind int

const (
	ObjectIterAll    ObjectIterKind = iota
	ObjectIterCommit
)

type ObjectIteratorOptions struct {
	Kind     ObjectIterKind
	MaxDepth *int
}

type oidQueueEntry struct {
	oid   string
	depth int
}

type ObjectIterator struct {
	repo     *Repo
	options  ObjectIteratorOptions
	queue    []oidQueueEntry
	excludes map[string]bool
}

func (repo *Repo) NewObjectIterator(opts ObjectIteratorOptions) *ObjectIterator {
	return &ObjectIterator{
		repo:     repo,
		options:  opts,
		excludes: make(map[string]bool),
	}
}

func (it *ObjectIterator) Close() {}

func (it *ObjectIterator) Include(oidHex string) {
	it.includeAtDepth(oidHex, 0)
}

func (it *ObjectIterator) includeAtDepth(oidHex string, depth int) {
	if it.options.MaxDepth != nil && depth > *it.options.MaxDepth {
		return
	}
	if !it.excludes[oidHex] {
		it.queue = append(it.queue, oidQueueEntry{oid: oidHex, depth: depth})
	}
}

func (it *ObjectIterator) Exclude(oidHex string) error {
	it.excludes[oidHex] = true

	obj, err := it.repo.NewObject(oidHex, true)
	if err != nil {
		return err
	}
	defer obj.Close()

	switch obj.Kind {
	case ObjectKindBlob, ObjectKindTag:
	case ObjectKindTree:
		if it.options.Kind == ObjectIterAll && obj.Tree != nil {
			for _, entry := range obj.Tree.Entries {
				if entry.Mode.ObjType() == ModeObjectTypeGitlink {
					continue
				}
				if err := it.Exclude(hex.EncodeToString(entry.OID)); err != nil {
					return err
				}
			}
		}
	case ObjectKindCommit:
		if obj.Commit != nil {
			for _, pid := range obj.Commit.ParentOIDs {
				it.excludes[pid] = true
			}
			if it.options.Kind == ObjectIterAll {
				if err := it.Exclude(obj.Commit.Tree); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Next returns the next object in raw mode. The caller must Close the returned Object.
func (it *ObjectIterator) Next() (*Object, error) {
	for len(it.queue) > 0 {
		entry := it.queue[0]
		it.queue = it.queue[1:]

		if it.excludes[entry.oid] {
			continue
		}
		it.excludes[entry.oid] = true

		fullObj, err := it.repo.NewObject(entry.oid, true)
		if err != nil {
			return nil, err
		}

		it.includeContentRefs(fullObj, entry.depth+1)

		if it.options.Kind == ObjectIterCommit && fullObj.Kind != ObjectKindCommit {
			fullObj.Close()
			continue
		}

		fullObj.Close()

		rawObj, err := it.repo.NewObject(entry.oid, false)
		if err != nil {
			return nil, err
		}
		return rawObj, nil
	}
	return nil, nil
}

func (it *ObjectIterator) includeContentRefs(obj *Object, childDepth int) {
	switch obj.Kind {
	case ObjectKindBlob:
	case ObjectKindTree:
		if it.options.Kind == ObjectIterAll && obj.Tree != nil {
			for _, entry := range obj.Tree.Entries {
				if entry.Mode.ObjType() == ModeObjectTypeGitlink {
					continue
				}
				it.includeAtDepth(hex.EncodeToString(entry.OID), childDepth)
			}
		}
	case ObjectKindCommit:
		if obj.Commit != nil {
			for _, pid := range obj.Commit.ParentOIDs {
				it.includeAtDepth(pid, childDepth)
			}
			if it.options.Kind == ObjectIterAll {
				it.includeAtDepth(obj.Commit.Tree, childDepth)
			}
		}
	case ObjectKindTag:
		if obj.Tag != nil {
			it.includeAtDepth(obj.Tag.Target, childDepth)
		}
	}
}

// ---------------------------------------------------------------------------
// CopyFromPackIterator – writes pack objects as loose objects
// ---------------------------------------------------------------------------

func (repo *Repo) CopyFromPackIterator(iter *PackIterator) error {
	offsetToOID := make(map[uint64][]byte)

	for {
		por, err := iter.Next(repo, offsetToOID)
		if err != nil {
			return err
		}
		if por == nil {
			break
		}

		startPos := iter.StartPosition()
		header := por.Header()

		oidBytes, err := repo.writeObject(header, por)
		por.Close()
		if err != nil {
			return err
		}

		offsetToOID[startPos] = oidBytes
	}
	return nil
}
