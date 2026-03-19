package gitgonano

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

func RefFromPath(refPath string, defaultKind *RefKind) *Ref {
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

func RefOrOidFromDb(content string) *RefOrOid {
	if strings.HasPrefix(content, refStartStr) {
		ref := RefFromPath(content[len(refStartStr):], nil)
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

func ValidateRefName(name string) bool {
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

// ReadRef reads a ref from the repo dir.
func ReadRef(repoDir string, refPath string) (*RefOrOid, error) {
	filePath := filepath.Join(repoDir, refPath)
	data, err := os.ReadFile(filePath)
	if err == nil {
		content := strings.TrimRight(string(data), "\n\r")
		result := RefOrOidFromDb(content)
		return result, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	// look for packed refs
	packedRefsPath := filepath.Join(repoDir, "packed-refs")
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

// ReadRefRecur recursively resolves a RefOrOid to an OID hex string.
// Returns "" if the ref chain ends without an OID.
func ReadRefRecur(repoDir string, input RefOrOid) (string, error) {
	if !input.IsRef {
		return input.OID, nil
	}

	refPath := input.Ref.ToPath()
	result, err := ReadRef(repoDir, refPath)
	if err != nil {
		if errors.Is(err, ErrRefNotFound) {
			return "", nil
		}
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return ReadRefRecur(repoDir, *result)
}

// ReadHeadRecurMaybe reads HEAD and recursively resolves it.
// Returns "" if HEAD doesn't resolve to an OID (e.g. new repo with no commits).
func ReadHeadRecurMaybe(repoDir string) (string, error) {
	result, err := ReadRef(repoDir, "HEAD")
	if err != nil {
		if errors.Is(err, ErrRefNotFound) {
			return "", nil
		}
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return ReadRefRecur(repoDir, *result)
}

// ReadHeadRecur reads HEAD and recursively resolves it.
// Returns error if HEAD doesn't resolve to an OID.
func ReadHeadRecur(repoDir string) (string, error) {
	oid, err := ReadHeadRecurMaybe(repoDir)
	if err != nil {
		return "", err
	}
	if oid == "" {
		return "", errors.New("HEAD has no valid hash")
	}
	return oid, nil
}

// WriteRef writes a ref (OID or symbolic ref) to the repo.
func WriteRef(repoDir string, refPath string, refOrOid RefOrOid) error {
	fullPath := filepath.Join(repoDir, refPath)
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

	lock, err := NewLockFile(repoDir, refPath)
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

// WriteRefRecur recursively follows symbolic refs and writes the OID.
func WriteRefRecur(repoDir string, refPath string, oidHex string) error {
	result, err := ReadRef(repoDir, refPath)
	if err != nil {
		if errors.Is(err, ErrRefNotFound) {
			return WriteRef(repoDir, refPath, RefOrOid{OID: oidHex})
		}
		return err
	}
	if result == nil {
		return WriteRef(repoDir, refPath, RefOrOid{OID: oidHex})
	}
	if result.IsRef {
		nextRefPath := result.Ref.ToPath()
		return WriteRefRecur(repoDir, nextRefPath, oidHex)
	}
	return WriteRef(repoDir, refPath, RefOrOid{OID: oidHex})
}

// ReplaceHead writes a ref or OID to HEAD.
func ReplaceHead(repoDir string, refOrOid RefOrOid) error {
	return WriteRef(repoDir, "HEAD", refOrOid)
}

// UpdateHead writes an OID to HEAD (following symbolic refs).
func UpdateHead(repoDir string, oidHex string) error {
	return WriteRefRecur(repoDir, "HEAD", oidHex)
}

// RefExists checks whether a ref exists.
func RefExists(repoDir string, ref Ref) (bool, error) {
	refPath := ref.ToPath()
	_, err := ReadRef(repoDir, refPath)
	if err != nil {
		if errors.Is(err, ErrRefNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
