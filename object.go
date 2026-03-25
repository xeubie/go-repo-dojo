package repomofo

import (
	"bytes"
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

func objectKindFromName(name string) (ObjectKind, error) {
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

// parseObjectHeader parses a git object header from decompressed data.
func parseObjectHeader(data []byte) (ObjectHeader, int, error) {
	spaceIdx := bytes.IndexByte(data, ' ')
	if spaceIdx < 0 {
		return ObjectHeader{}, 0, errors.New("invalid object header: no space")
	}

	kind, err := objectKindFromName(string(data[:spaceIdx]))
	if err != nil {
		return ObjectHeader{}, 0, err
	}

	nullIdx := bytes.IndexByte(data[spaceIdx+1:], 0)
	if nullIdx < 0 {
		return ObjectHeader{}, 0, errors.New("invalid object header: no null")
	}
	nullIdx += spaceIdx + 1

	size, err := strconv.ParseUint(string(data[spaceIdx+1:nullIdx]), 10, 64)
	if err != nil {
		return ObjectHeader{}, 0, fmt.Errorf("invalid object size: %w", err)
	}

	return ObjectHeader{Kind: kind, Size: size}, nullIdx + 1, nil
}

// readObjectKind reads an object and returns just its kind.
func (repo *Repo) readObjectKind(oid Hash) (ObjectKind, error) {
	rdr, err := repo.store.ReadObject(oid)
	if err != nil {
		return 0, err
	}
	defer rdr.Close()
	return rdr.Header().Kind, nil
}

// readCommitTree reads a commit object and returns its tree hash.
func (repo *Repo) readCommitTree(oid Hash) (Hash, error) {
	rdr, err := repo.store.ReadObject(oid)
	if err != nil {
		return nil, err
	}
	defer rdr.Close()

	data, err := io.ReadAll(rdr)
	if err != nil {
		return nil, err
	}

	if !bytes.HasPrefix(data, []byte("tree ")) {
		return nil, errors.New("invalid commit: missing tree")
	}
	hexLen := repo.opts.Hash.HexLen()
	if len(data) < 5+hexLen {
		return nil, errors.New("invalid commit: tree hash too short")
	}
	treeHash, err := repo.opts.Hash.HashFromHex(string(data[5 : 5+hexLen]))
	if err != nil {
		return nil, err
	}
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

func (t *treeBuilder) addBlobEntry(mode Mode, name string, oid Hash) {
	header := fmt.Sprintf("%s %s\x00", mode.String(), name)
	oidBytes := oid.Bytes()
	data := make([]byte, len(header)+len(oidBytes))
	copy(data, header)
	copy(data[len(header):], oidBytes)
	t.entries = append(t.entries, treeBuilderEntry{
		sortKey: name,
		data:    data,
	})
}

func (t *treeBuilder) addTreeEntry(name string, oid Hash) {
	header := fmt.Sprintf("40000 %s\x00", name)
	oidBytes := oid.Bytes()
	data := make([]byte, len(header)+len(oidBytes))
	copy(data, header)
	copy(data[len(header):], oidBytes)
	t.entries = append(t.entries, treeBuilderEntry{
		sortKey: name + "/",
		data:    data,
	})
}

func (t *treeBuilder) addIndexEntries(repo *Repo, idx *index, prefix string, childNames []string) error {
	for _, name := range childNames {
		var path string
		if prefix == "" {
			path = name
		} else {
			path = joinPath([]string{prefix, name})
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

func (repo *Repo) writeTree(tree *treeBuilder) (Hash, error) {
	sort.Slice(tree.entries, func(i, j int) bool {
		return tree.entries[i].sortKey < tree.entries[j].sortKey
	})

	var content bytes.Buffer
	for _, e := range tree.entries {
		content.Write(e.data)
	}

	return repo.store.WriteObject(
		ObjectHeader{Kind: ObjectKindTree, Size: uint64(content.Len())},
		&content,
	)
}

func (repo *Repo) writeBlob(content []byte) (Hash, error) {
	return repo.store.WriteObject(
		ObjectHeader{Kind: ObjectKindBlob, Size: uint64(len(content))},
		bytes.NewReader(content),
	)
}

func (repo *Repo) writeBlobFromReader(reader io.Reader, size uint64) (Hash, error) {
	return repo.store.WriteObject(
		ObjectHeader{Kind: ObjectKindBlob, Size: size},
		reader,
	)
}

// CommitMetadata holds metadata for creating a commit.
type CommitMetadata struct {
	Author     string
	Committer  string
	Message    string
	ParentOIDs []Hash // nil means "use HEAD"
	AllowEmpty bool
}

// signContent signs content using ssh-keygen and returns the signature lines.
func (repo *Repo) signContent(lines []string, signingKey string) ([]string, error) {
	content := strings.Join(lines, "\n")

	contentFileName := "git_signing_buffer"
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
	sigFilePath := filepath.Join(repo.repoPath, sigFileName)
	sigData, err := os.ReadFile(sigFilePath)
	if err != nil {
		return nil, err
	}
	defer os.Remove(sigFilePath)

	sigContent := strings.TrimRight(string(sigData), "\n")
	return strings.Split(sigContent, "\n"), nil
}

// writeCommit creates a new commit object and updates HEAD.
func (repo *Repo) writeCommit(metadata CommitMetadata) (Hash, error) {
	parentOIDs := metadata.ParentOIDs
	if parentOIDs == nil {
		headOID, err := repo.ReadHeadRecurMaybe()
		if err != nil {
			return nil, err
		}
		if headOID != nil {
			parentOIDs = []Hash{headOID}
		} else {
			parentOIDs = []Hash{}
		}
	}

	if err := repo.checkForUnfinishedMerge(); err != nil {
		return nil, err
	}

	idx, err := repo.readIndex()
	if err != nil {
		return nil, err
	}

	var rootChildNames []string
	for k := range idx.rootChildren {
		rootChildNames = append(rootChildNames, k)
	}

	tree := newTreeBuilder()
	if err := tree.addIndexEntries(repo, idx, "", rootChildNames); err != nil {
		return nil, err
	}
	treeOID, err := repo.writeTree(tree)
	if err != nil {
		return nil, err
	}

	if !metadata.AllowEmpty {
		if len(parentOIDs) == 0 {
			if len(rootChildNames) == 0 {
				return nil, errors.New("empty commit")
			}
		} else if len(parentOIDs) == 1 {
			parentTree, err := repo.readCommitTree(parentOIDs[0])
			if err != nil {
				return nil, err
			}
			if HashEqual(parentTree, treeOID) {
				return nil, errors.New("empty commit")
			}
		}
	}

	config, err := repo.loadConfig()
	if err != nil {
		return nil, err
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("tree %s", treeOID.Hex()))
	for _, parent := range parentOIDs {
		lines = append(lines, fmt.Sprintf("parent %s", parent.Hex()))
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
				return nil, errors.New("user config not found")
			}
			name, ok1 := userSection["name"]
			email, ok2 := userSection["email"]
			if !ok1 || !ok2 {
				return nil, errors.New("user config not found")
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
				return nil, err
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

	oid, err := repo.store.WriteObject(
		ObjectHeader{Kind: ObjectKindCommit, Size: uint64(len(commitContent))},
		strings.NewReader(commitContent),
	)
	if err != nil {
		return nil, err
	}

	if err := repo.writeRefRecur("HEAD", oid); err != nil {
		return nil, err
	}

	return oid, nil
}

// writeTag creates a new tag object. Returns the tag Hash.
func (repo *Repo) writeTag(input AddTagInput, targetOID Hash) (Hash, error) {
	targetKind, err := repo.readObjectKind(targetOID)
	if err != nil {
		return nil, err
	}

	config, err := repo.loadConfig()
	if err != nil {
		return nil, err
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("object %s", targetOID.Hex()))
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
				return nil, errors.New("user config not found")
			}
			name, ok1 := userSection["name"]
			email, ok2 := userSection["email"]
			if !ok1 || !ok2 {
				return nil, errors.New("user config not found")
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
				return nil, err
			}
			lines = append(lines, sigLines...)
		}
	}

	tagContent := strings.Join(lines, "\n")

	return repo.store.WriteObject(
		ObjectHeader{Kind: ObjectKindTag, Size: uint64(len(tagContent))},
		strings.NewReader(tagContent),
	)
}

// ---------------------------------------------------------------------------
// ObjectReader – streaming reader for a single git object
// ---------------------------------------------------------------------------

// ObjectReader reads an object from loose storage or from a pack file.
type ObjectReader interface {
	Close() error
	Header() ObjectHeader
	Reset() error
	Read(p []byte) (int, error)
	SkipBytes(n uint64) error
	Position() uint64
}

// ---------------------------------------------------------------------------
// ObjectStore – pluggable object storage backend
// ---------------------------------------------------------------------------

// ObjectStore is the interface for pluggable object storage backends.
type ObjectStore interface {
	// ReadObject returns a streaming reader for the object with the given OID.
	ReadObject(oid Hash) (ObjectReader, error)

	// WriteObject writes an object and returns its Hash.
	WriteObject(header ObjectHeader, reader io.Reader) (Hash, error)
}

// Returns a streaming reader for the object with the given OID.
func (repo *Repo) NewObjectReader(oid Hash) (ObjectReader, error) {
	return repo.store.ReadObject(oid)
}

// ---------------------------------------------------------------------------
// Parsed object content types
// ---------------------------------------------------------------------------

type CommitContent struct {
	Tree       Hash
	ParentOIDs []Hash
	Author     string
	Committer  string
	Message    string
	Timestamp  uint64 // committer timestamp (unix epoch seconds)
}

type TreeContentEntry struct {
	Name string
	Mode Mode
	OID  Hash
}

type TreeContent struct {
	Entries []TreeContentEntry
}

type TagContent struct {
	Target  Hash
	Kind    ObjectKind
	Name    string
	Tagger  string
	Message string
}

// ---------------------------------------------------------------------------
// Object – a fully or partially parsed git object
// ---------------------------------------------------------------------------

type Object struct {
	OID  Hash
	Kind ObjectKind
	Size uint64

	// Full-mode parsed content (nil for raw mode)
	Commit *CommitContent
	Tree   *TreeContent
	Tag    *TagContent

	reader ObjectReader
}

// Reads an object by OID, optionally parsing its content when full is true.
func (repo *Repo) NewObject(oid Hash, full bool) (*Object, error) {
	rdr, err := repo.NewObjectReader(oid)
	if err != nil {
		return nil, err
	}
	header := rdr.Header()

	obj := &Object{
		OID:    oid,
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

func (o *Object) Close() error {
	if o.reader != nil {
		return o.reader.Close()
	}
	return nil
}

func (o *Object) parseContent(hashKind HashKind) error {
	switch o.Kind {
	case ObjectKindBlob:
		return nil
	case ObjectKindTree:
		return o.parseTree(hashKind)
	case ObjectKindCommit:
		return o.parseCommit(hashKind)
	case ObjectKindTag:
		return o.parseTag(hashKind)
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
		oid := hashKind.HashFromBytes(data[pos : pos+byteLen])
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

func (o *Object) parseCommit(hashKind HashKind) error {
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
			h, err := hashKind.HashFromHex(line[5:])
			if err == nil {
				cc.Tree = h
			}
		} else if strings.HasPrefix(line, "parent ") {
			h, err := hashKind.HashFromHex(line[7:])
			if err == nil {
				cc.ParentOIDs = append(cc.ParentOIDs, h)
			}
		} else if strings.HasPrefix(line, "author ") {
			cc.Author = line[7:]
		} else if strings.HasPrefix(line, "committer ") {
			cc.Committer = line[10:]
			cc.Timestamp = parseIdentTimestamp(line[10:])
		}
	}

	o.Commit = cc
	return nil
}

// parseIdentTimestamp extracts the unix timestamp from a git ident line
// of the form "Name <email> timestamp timezone".
func parseIdentTimestamp(ident string) uint64 {
	// find last two space-separated tokens: "timestamp timezone"
	parts := strings.Split(ident, " ")
	if len(parts) < 3 {
		return 0
	}
	tsStr := parts[len(parts)-2]
	ts, err := strconv.ParseUint(tsStr, 10, 64)
	if err != nil {
		return 0
	}
	return ts
}

func (o *Object) parseTag(hashKind HashKind) error {
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
			h, err := hashKind.HashFromHex(line[7:])
			if err == nil {
				tc.Target = h
			}
		} else if strings.HasPrefix(line, "type ") {
			k, err := objectKindFromName(line[5:])
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
	ObjectIterAll ObjectIterKind = iota
	ObjectIterCommit
)

type ObjectIteratorOptions struct {
	Kind     ObjectIterKind
	MaxDepth int  // 0 means unlimited (default)
	Full     bool // if true, Next returns parsed objects; if false, returns raw objects
}

type oidQueueEntry struct {
	oid   Hash
	depth int
}

type ObjectIterator struct {
	repo     *Repo
	options  ObjectIteratorOptions
	queue    []oidQueueEntry
	Excludes map[string]bool
	Depth    int // depth of the last object returned by Next
}

// Creates a graph-walking iterator over reachable objects.
func (repo *Repo) NewObjectIterator(opts ObjectIteratorOptions) *ObjectIterator {
	return &ObjectIterator{
		repo:     repo,
		options:  opts,
		Excludes: make(map[string]bool),
	}
}

func (it *ObjectIterator) Include(oid Hash) {
	it.IncludeAtDepth(oid, 0)
}

func (it *ObjectIterator) IncludeAtDepth(oid Hash, depth int) {
	if it.options.MaxDepth > 0 && depth > it.options.MaxDepth {
		return
	}
	hex := oid.Hex()
	if !it.Excludes[hex] {
		it.queue = append(it.queue, oidQueueEntry{oid: oid, depth: depth})
	}
}

func (it *ObjectIterator) Exclude(oid Hash) error {
	it.Excludes[oid.Hex()] = true

	obj, err := it.repo.NewObject(oid, true)
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
				if err := it.Exclude(entry.OID); err != nil {
					return err
				}
			}
		}
	case ObjectKindCommit:
		if obj.Commit != nil {
			for _, pid := range obj.Commit.ParentOIDs {
				it.Excludes[pid.Hex()] = true
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

		hex := entry.oid.Hex()
		if it.Excludes[hex] {
			continue
		}
		it.Excludes[hex] = true

		fullObj, err := it.repo.NewObject(entry.oid, true)
		if err != nil {
			return nil, err
		}

		it.includeContentRefs(fullObj, entry.depth+1)
		it.Depth = entry.depth

		if it.options.Kind == ObjectIterCommit && fullObj.Kind != ObjectKindCommit {
			fullObj.Close()
			continue
		}

		if it.options.Full {
			return fullObj, nil
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
				it.IncludeAtDepth(entry.OID, childDepth)
			}
		}
	case ObjectKindCommit:
		if obj.Commit != nil {
			for _, pid := range obj.Commit.ParentOIDs {
				it.IncludeAtDepth(pid, childDepth)
			}
			if it.options.Kind == ObjectIterAll {
				it.IncludeAtDepth(obj.Commit.Tree, childDepth)
			}
		}
	case ObjectKindTag:
		if obj.Tag != nil {
			it.IncludeAtDepth(obj.Tag.Target, childDepth)
		}
	}
}

// Writes each object from a pack iterator as a loose object in the store.
func (repo *Repo) CopyFromPackIterator(iter *PackIterator) error {
	if fs, ok := repo.store.(*fileObjectStore); ok {
		return fs.CopyFromPackIterator(iter)
	}
	// Generic fallback: read each object and write through the store.
	offsetToOID := make(map[uint64]Hash)
	for {
		por, err := iter.Next(repo.store, offsetToOID)
		if err != nil {
			return err
		}
		if por == nil {
			break
		}

		startPos := iter.StartPosition()
		header := por.Header()

		oid, err := repo.store.WriteObject(header, por)
		por.Close()
		if err != nil {
			return err
		}

		offsetToOID[startPos] = oid
	}
	return nil
}
