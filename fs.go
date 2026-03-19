package gitgonano

import (
	"os"
	"path/filepath"
	"strings"
)

// Mode represents a git file mode as a packed uint32.
// Bit layout (from LSB):
//
//	bits 0-8:   unix permission (9 bits)
//	bits 9-11:  unused (3 bits)
//	bits 12-15: object type (4 bits)
//	bits 16-31: padding (16 bits)
type Mode uint32

type ModeObjectType uint8

const (
	ModeObjectTypeTree        ModeObjectType = 0o04
	ModeObjectTypeRegularFile ModeObjectType = 0o10
	ModeObjectTypeSymlink     ModeObjectType = 0o12
	ModeObjectTypeGitlink     ModeObjectType = 0o16
)

func (m Mode) ObjType() ModeObjectType {
	return ModeObjectType((m >> 12) & 0xF)
}

func (m Mode) UnixPerm() uint16 {
	return uint16(m & 0x1FF)
}

func (m Mode) String() string {
	switch m.ObjType() {
	case ModeObjectTypeTree:
		return "40000"
	case ModeObjectTypeRegularFile:
		if m.UnixPerm() == 0o755 {
			return "100755"
		}
		return "100644"
	case ModeObjectTypeSymlink:
		return "120000"
	case ModeObjectTypeGitlink:
		return "160000"
	}
	return "100644"
}

func ModeFromFileInfo(info os.FileInfo) Mode {
	if info.Mode()&os.ModeSymlink != 0 {
		return Mode(0o120000)
	}
	if info.Mode().Perm()&0o100 != 0 {
		return Mode(0o100755)
	}
	return Mode(0o100644)
}

// LockFile implements atomic file writes via a temporary .lock file.
// On Close, if Success is true, the lock file is renamed to the target;
// otherwise it is deleted.
type LockFile struct {
	dir      string
	fileName string
	lockPath string
	File     *os.File
	Success  bool
}

func NewLockFile(dir, fileName string) (*LockFile, error) {
	lockPath := filepath.Join(dir, fileName+".lock")
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		return nil, err
	}
	return &LockFile{
		dir:      dir,
		fileName: fileName,
		lockPath: lockPath,
		File:     file,
	}, nil
}

func (l *LockFile) Close() {
	l.File.Close()
	if l.Success {
		targetPath := filepath.Join(l.dir, l.fileName)
		if err := os.Rename(l.lockPath, targetPath); err != nil {
			l.Success = false
		}
	}
	if !l.Success {
		os.Remove(l.lockPath)
	}
}

// JoinPath joins path components with "/" (always forward slash).
// Empty components and "." are skipped.
func JoinPath(parts []string) string {
	var nonEmpty []string
	for _, p := range parts {
		if p != "" && p != "." {
			nonEmpty = append(nonEmpty, p)
		}
	}
	if len(nonEmpty) == 0 {
		return "."
	}
	return strings.Join(nonEmpty, "/")
}

// SplitPath splits a forward-slash path into its components.
func SplitPath(path string) []string {
	path = filepath.ToSlash(path)
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	return parts
}
