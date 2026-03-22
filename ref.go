package repomofo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrRefNotFound = errors.New("ref not found")
)

const refStartStr = "ref: "

func validateRefName(name string) bool {
	if len(name) == 0 || len(name) > 255 {
		return false
	}
	if name[0] == '-' || name[len(name)-1] == '.' {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "@{") {
		return false
	}
	for _, c := range name {
		if c <= 0x1F || c == 0x7F || c == ' ' || c == '~' || c == '^' ||
			c == ':' || c == '?' || c == '*' || c == '[' || c == '\\' {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if len(part) == 0 || part[0] == '.' || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

type RefKind int

const (
	RefNone RefKind = iota
	RefHead
	RefTag
	RefRemote
	RefOther
)

type Ref struct {
	Kind       RefKind
	Name       string
	RemoteName string // only used when Kind == RefRemote
}

func (r Ref) ToPath() string {
	switch r.Kind {
	case RefNone:
		return r.Name
	case RefHead:
		return "refs/heads/" + r.Name
	case RefTag:
		return "refs/tags/" + r.Name
	case RefRemote:
		return "refs/remotes/" + r.RemoteName + "/" + r.Name
	case RefOther:
		return "refs/" + r.Name
	}
	return r.Name
}

func refFromPath(refPath string, defaultKind *RefKind) *Ref {
	parts := strings.Split(refPath, "/")

	if parts[0] != "refs" {
		// unqualified refs like HEAD, MERGE_HEAD, CHERRY_PICK_HEAD
		if len(parts) == 1 {
			switch refPath {
			case "HEAD", "MERGE_HEAD", "CHERRY_PICK_HEAD":
				return &Ref{Kind: RefNone, Name: refPath}
			}
		}
		if defaultKind != nil {
			return &Ref{Kind: *defaultKind, Name: refPath}
		}
		return nil
	}

	if len(parts) < 3 {
		return nil
	}

	refKindStr := parts[1]
	refName := strings.Join(parts[2:], "/")

	switch refKindStr {
	case "heads":
		return &Ref{Kind: RefHead, Name: refName}
	case "tags":
		return &Ref{Kind: RefTag, Name: refName}
	case "remotes":
		if len(parts) < 4 {
			return nil
		}
		remoteName := parts[2]
		remoteRefName := strings.Join(parts[3:], "/")
		return &Ref{Kind: RefRemote, Name: remoteRefName, RemoteName: remoteName}
	default:
		return &Ref{Kind: RefOther, Name: refName}
	}
}

// RefOrOid represents either a symbolic ref or an object ID.
type RefOrOid struct {
	IsRef bool
	Ref   Ref
	OID   string // hex string
}

func refOrOidFromDb(content string) *RefOrOid {
	if strings.HasPrefix(content, refStartStr) {
		ref := refFromPath(content[len(refStartStr):], nil)
		if ref == nil {
			return nil
		}
		return &RefOrOid{IsRef: true, Ref: *ref}
	}
	if isHexString(content) && (len(content) == 40 || len(content) == 64) {
		return &RefOrOid{OID: content}
	}
	return nil
}

func isHexString(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// RefIterator iterates over refs in a directory using a stack-based
// depth-first traversal, so it doesn't load all refs into memory at once.
type RefIterator struct {
	stack   []refIterStackEntry
	refKind RefKind
}

type refIterStackEntry struct {
	dir     *os.File
	entries []os.DirEntry
	index   int
	prefix  string
}

func newRefIterator(dir string, refKind RefKind) (*RefIterator, error) {
	f, err := os.Open(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &RefIterator{refKind: refKind}, nil
		}
		return nil, err
	}
	entries, err := f.ReadDir(-1)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &RefIterator{
		stack:   []refIterStackEntry{{dir: f, entries: entries, prefix: ""}},
		refKind: refKind,
	}, nil
}

// Next returns the next ref, or nil when iteration is complete.
func (it *RefIterator) Next() (*Ref, error) {
	for len(it.stack) > 0 {
		top := &it.stack[len(it.stack)-1]
		if top.index >= len(top.entries) {
			top.dir.Close()
			it.stack = it.stack[:len(it.stack)-1]
			continue
		}
		entry := top.entries[top.index]
		top.index++

		name := entry.Name()
		fullPrefix := name
		if top.prefix != "" {
			fullPrefix = top.prefix + "/" + name
		}

		if entry.IsDir() {
			path := filepath.Join(top.dir.Name(), name)
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			entries, err := f.ReadDir(-1)
			if err != nil {
				f.Close()
				return nil, err
			}
			it.stack = append(it.stack, refIterStackEntry{
				dir:     f,
				entries: entries,
				prefix:  fullPrefix,
			})
			continue
		}

		ref := Ref{Kind: it.refKind, Name: fullPrefix}
		return &ref, nil
	}
	return nil, nil
}

// Close releases all open directory handles.
func (it *RefIterator) Close() {
	for _, entry := range it.stack {
		entry.dir.Close()
	}
	it.stack = nil
}

// readRef reads a ref from the repo dir.
func (repo *Repo) readRef(refPath string) (*RefOrOid, error) {
	filePath := filepath.Join(repo.repoPath, refPath)
	data, err := os.ReadFile(filePath)
	if err == nil {
		content := strings.TrimRight(string(data), "\n\r")
		result := refOrOidFromDb(content)
		return result, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	// look for packed refs
	packedRefsPath := filepath.Join(repo.repoPath, "packed-refs")
	packedData, err := os.ReadFile(packedRefsPath)
	if err == nil {
		lines := strings.Split(string(packedData), "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || trimmed == "" {
				continue
			}
			parts := strings.SplitN(trimmed, " ", 2)
			if len(parts) == 2 && isHexString(parts[0]) && parts[1] == refPath {
				return &RefOrOid{OID: parts[0]}, nil
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	return nil, ErrRefNotFound
}

// readRefRecur recursively resolves a RefOrOid to an OID hex string.
func (repo *Repo) readRefRecur(input RefOrOid) (string, error) {
	if !input.IsRef {
		return input.OID, nil
	}

	refPath := input.Ref.ToPath()
	result, err := repo.readRef(refPath)
	if err != nil {
		if errors.Is(err, ErrRefNotFound) {
			return "", nil
		}
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return repo.readRefRecur(*result)
}

// Resolves HEAD recursively to an OID hex string, returning "" if unresolvable.
func (repo *Repo) ReadHeadRecurMaybe() (string, error) {
	result, err := repo.readRef("HEAD")
	if err != nil {
		if errors.Is(err, ErrRefNotFound) {
			return "", nil
		}
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return repo.readRefRecur(*result)
}

// Resolves HEAD recursively to an OID hex string, returning an error if unresolvable.
func (repo *Repo) ReadHeadRecur() (string, error) {
	oid, err := repo.ReadHeadRecurMaybe()
	if err != nil {
		return "", err
	}
	if oid == "" {
		return "", errors.New("HEAD has no valid hash")
	}
	return oid, nil
}

// Recursively resolves a ref to its OID hex string.
func (repo *Repo) ReadRef(ref Ref) (string, error) {
	refPath := ref.ToPath()
	result, err := repo.readRef(refPath)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return repo.readRefRecur(*result)
}

// writeRef writes a ref (OID or symbolic ref) to the repo.
func (repo *Repo) writeRef(refPath string, refOrOid RefOrOid) error {
	fullPath := filepath.Join(repo.repoPath, refPath)
	parentDir := filepath.Dir(fullPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return err
	}

	var content string
	if refOrOid.IsRef {
		content = refStartStr + refOrOid.Ref.ToPath()
	} else {
		content = refOrOid.OID
	}

	lock, err := newLockFile(repo.repoPath, refPath)
	if err != nil {
		return err
	}
	defer lock.Close()

	if _, err := lock.File.WriteString(content + "\n"); err != nil {
		return err
	}
	lock.Success = true
	return nil
}

// writeRefRecur recursively follows symbolic refs and writes the OID.
func (repo *Repo) writeRefRecur(refPath string, oidHex string) error {
	result, err := repo.readRef(refPath)
	if err != nil {
		if errors.Is(err, ErrRefNotFound) {
			return repo.writeRef(refPath, RefOrOid{OID: oidHex})
		}
		return err
	}
	if result == nil {
		return repo.writeRef(refPath, RefOrOid{OID: oidHex})
	}
	if result.IsRef {
		nextRefPath := result.Ref.ToPath()
		return repo.writeRefRecur(nextRefPath, oidHex)
	}
	return repo.writeRef(refPath, RefOrOid{OID: oidHex})
}

// replaceHead writes a ref or OID to HEAD.
func (repo *Repo) replaceHead(refOrOid RefOrOid) error {
	return repo.writeRef("HEAD", refOrOid)
}

// updateHead writes an OID to HEAD (following symbolic refs).
func (repo *Repo) updateHead(oidHex string) error {
	return repo.writeRefRecur("HEAD", oidHex)
}

// refExists checks whether a ref exists.
func (repo *Repo) refExists(ref Ref) (bool, error) {
	refPath := ref.ToPath()
	_, err := repo.readRef(refPath)
	if err != nil {
		if errors.Is(err, ErrRefNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
