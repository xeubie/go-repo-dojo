package repodojo

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
)

type IndexEntry struct {
	ctimeSecs  uint32
	ctimeNsecs uint32
	mtimeSecs  uint32
	mtimeNsecs uint32
	dev        uint32
	ino        uint32
	mode       Mode
	uid        uint32
	gid        uint32
	fileSize   uint32
	oid        []byte
	flags      uint16
	path       string
}

// Index represents a parsed git index file.
type Index struct {
	repo          *Repo
	version       uint32
	entries       map[string][4]*IndexEntry
	dirToPaths    map[string]map[string]bool
	dirToChildren map[string]map[string]bool
	rootChildren  map[string]bool
}

// readIndex reads and parses the git index file.
func (repo *Repo) readIndex() (*Index, error) {
	hashKind := repo.opts.Hash
	idx := &Index{
		repo:          repo,
		version:       2,
		entries:       make(map[string][4]*IndexEntry),
		dirToPaths:    make(map[string]map[string]bool),
		dirToChildren: make(map[string]map[string]bool),
		rootChildren:  make(map[string]bool),
	}

	indexPath := filepath.Join(repo.repoDir, "index")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return idx, nil
		}
		return nil, err
	}

	if len(data) < 12 {
		return nil, fmt.Errorf("index file too short")
	}

	// check signature
	if string(data[:4]) != "DIRC" {
		return nil, fmt.Errorf("invalid index signature")
	}

	idx.version = binary.BigEndian.Uint32(data[4:8])
	if idx.version != 2 {
		return nil, fmt.Errorf("unsupported index version: %d", idx.version)
	}

	entryCount := binary.BigEndian.Uint32(data[8:12])
	offset := 12

	byteLen := hashKind.ByteLen()

	for i := uint32(0); i < entryCount; i++ {
		startOffset := offset

		if offset+40+byteLen+2 > len(data) {
			return nil, fmt.Errorf("truncated index entry")
		}

		entry := &IndexEntry{
			ctimeSecs:  binary.BigEndian.Uint32(data[offset:]),
			ctimeNsecs: binary.BigEndian.Uint32(data[offset+4:]),
			mtimeSecs:  binary.BigEndian.Uint32(data[offset+8:]),
			mtimeNsecs: binary.BigEndian.Uint32(data[offset+12:]),
			dev:        binary.BigEndian.Uint32(data[offset+16:]),
			ino:        binary.BigEndian.Uint32(data[offset+20:]),
			mode:       Mode(binary.BigEndian.Uint32(data[offset+24:])),
			uid:        binary.BigEndian.Uint32(data[offset+28:]),
			gid:        binary.BigEndian.Uint32(data[offset+32:]),
			fileSize:   binary.BigEndian.Uint32(data[offset+36:]),
		}
		offset += 40

		entry.oid = make([]byte, byteLen)
		copy(entry.oid, data[offset:offset+byteLen])
		offset += byteLen

		entry.flags = binary.BigEndian.Uint16(data[offset:])
		offset += 2

		// read path (null-terminated)
		nullIdx := bytes.IndexByte(data[offset:], 0)
		if nullIdx < 0 {
			return nil, fmt.Errorf("unterminated path in index entry")
		}
		entry.path = string(data[offset : offset+nullIdx])
		offset += nullIdx + 1 // skip path + null

		// validate mode: normalize permission to 644 if not 755
		if entry.mode.UnixPerm() != 0o755 {
			entry.mode = Mode((uint32(entry.mode) & ^uint32(0x1FF)) | 0o644)
		}

		// skip padding to 8-byte boundary
		entrySize := offset - startOffset
		paddingBytes := (8 - (entrySize % 8)) % 8
		offset += paddingBytes

		idx.addEntry(entry)
	}

	return idx, nil
}

func (idx *Index) addEntry(entry *IndexEntry) {
	stage := (entry.flags >> 12) & 0x3

	entries, exists := idx.entries[entry.path]
	if exists {
		if stage == 0 {
			entries[1] = nil
			entries[2] = nil
			entries[3] = nil
		} else {
			entries[0] = nil
		}
		entries[stage] = entry
		idx.entries[entry.path] = entries
	} else {
		var newEntries [4]*IndexEntry
		newEntries[stage] = entry
		idx.entries[entry.path] = newEntries
	}

	// update directory maps
	child := path.Base(entry.path)
	parentPath := path.Dir(entry.path)

	for parentPath != "." && parentPath != "" {
		if _, ok := idx.dirToChildren[parentPath]; !ok {
			idx.dirToChildren[parentPath] = make(map[string]bool)
		}
		idx.dirToChildren[parentPath][child] = true

		if _, ok := idx.dirToPaths[parentPath]; !ok {
			idx.dirToPaths[parentPath] = make(map[string]bool)
		}
		idx.dirToPaths[parentPath][entry.path] = true

		child = path.Base(parentPath)
		parentPath = path.Dir(parentPath)
	}

	idx.rootChildren[child] = true
}

// AddPath adds a file from the working directory to the index.
// If the file doesn't exist on disk but is in the index, it gets removed.
func (idx *Index) AddPath(filePath string) error {
	repo := idx.repo
	fullPath := filepath.Join(repo.workPath, filePath)

	info, err := os.Lstat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			_, inEntries := idx.entries[filePath]
			inDir := idx.IsDir(filePath)
			if !inEntries && !inDir {
				return ErrAddIndexPathNotFound
			}
			idx.RemovePath(filePath, nil)
			return nil
		}
		return err
	}

	if info.IsDir() {
		dirEntries, err := os.ReadDir(fullPath)
		if err != nil {
			return err
		}
		for _, e := range dirEntries {
			if e.Name() == ".git" {
				continue
			}
			subPath := JoinPath([]string{filePath, e.Name()})
			if err := idx.AddPath(subPath); err != nil {
				return err
			}
		}
		return nil
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(fullPath)
		if err != nil {
			return err
		}
		oid, err := repo.writeBlob([]byte(target))
		if err != nil {
			return err
		}
		entry := &IndexEntry{
			mode:     Mode(0o120000),
			fileSize: uint32(len(target)),
			oid:      oid,
			flags:    uint16(len(filePath)) & 0xFFF,
			path:     filePath,
		}
		idx.addEntry(entry)
		return nil
	}

	// regular file
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return err
	}

	oid, err := repo.writeBlob(content)
	if err != nil {
		return err
	}

	mode := ModeFromFileInfo(info)
	// normalize permission
	if mode.UnixPerm() != 0o755 {
		mode = Mode(0o100644)
	}

	entry := &IndexEntry{
		mode:     mode,
		fileSize: uint32(info.Size()),
		oid:      oid,
		flags:    uint16(len(filePath)) & 0xFFF,
		path:     filePath,
	}

	idx.addEntry(entry)
	return nil
}

// RemovePath removes a path (or all paths under a directory) from the index.
// If removedPaths is non-nil, removed paths are recorded in it.
func (idx *Index) RemovePath(filePath string, removedPaths map[string]bool) {
	// check if it's a direct entry
	if _, ok := idx.entries[filePath]; ok {
		delete(idx.entries, filePath)
		if removedPaths != nil {
			removedPaths[filePath] = true
		}
	}

	// check if it's a directory prefix — remove all entries under it
	if paths, ok := idx.dirToPaths[filePath]; ok {
		for p := range paths {
			if _, ok := idx.entries[p]; ok {
				delete(idx.entries, p)
				if removedPaths != nil {
					removedPaths[p] = true
				}
			}
		}
	}

	// rebuild directory maps
	idx.rebuildDirMaps()
}

// IsDir returns true if the given path is a directory in the index.
func (idx *Index) IsDir(filePath string) bool {
	_, ok := idx.dirToPaths[filePath]
	return ok
}

func (idx *Index) rebuildDirMaps() {
	idx.dirToPaths = make(map[string]map[string]bool)
	idx.dirToChildren = make(map[string]map[string]bool)
	idx.rootChildren = make(map[string]bool)

	for p, entries := range idx.entries {
		for _, e := range entries {
			if e != nil {
				child := path.Base(p)
				parentPath := path.Dir(p)

				for parentPath != "." && parentPath != "" {
					if _, ok := idx.dirToChildren[parentPath]; !ok {
						idx.dirToChildren[parentPath] = make(map[string]bool)
					}
					idx.dirToChildren[parentPath][child] = true

					if _, ok := idx.dirToPaths[parentPath]; !ok {
						idx.dirToPaths[parentPath] = make(map[string]bool)
					}
					idx.dirToPaths[parentPath][p] = true

					child = path.Base(parentPath)
					parentPath = path.Dir(parentPath)
				}

				idx.rootChildren[child] = true
				break // only need one non-nil entry per path
			}
		}
	}
}

// Write serializes the index to the given file (typically a lock file).
func (idx *Index) Write(f *os.File) error {
	// collect and sort all entries
	type sortedEntry struct {
		path  string
		stage int
		entry *IndexEntry
	}
	var sorted []sortedEntry
	for p, entries := range idx.entries {
		for stage, entry := range entries {
			if entry != nil {
				sorted = append(sorted, sortedEntry{path: p, stage: stage, entry: entry})
			}
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].path != sorted[j].path {
			return sorted[i].path < sorted[j].path
		}
		return sorted[i].stage < sorted[j].stage
	})

	var buf bytes.Buffer

	// write header
	buf.WriteString("DIRC")
	binary.Write(&buf, binary.BigEndian, uint32(2))          // version
	binary.Write(&buf, binary.BigEndian, uint32(len(sorted))) // entry count

	// write entries
	for _, se := range sorted {
		entryStart := buf.Len()

		binary.Write(&buf, binary.BigEndian, se.entry.ctimeSecs)
		binary.Write(&buf, binary.BigEndian, se.entry.ctimeNsecs)
		binary.Write(&buf, binary.BigEndian, se.entry.mtimeSecs)
		binary.Write(&buf, binary.BigEndian, se.entry.mtimeNsecs)
		binary.Write(&buf, binary.BigEndian, se.entry.dev)
		binary.Write(&buf, binary.BigEndian, se.entry.ino)
		binary.Write(&buf, binary.BigEndian, uint32(se.entry.mode))
		binary.Write(&buf, binary.BigEndian, se.entry.uid)
		binary.Write(&buf, binary.BigEndian, se.entry.gid)
		binary.Write(&buf, binary.BigEndian, se.entry.fileSize)
		buf.Write(se.entry.oid)
		binary.Write(&buf, binary.BigEndian, se.entry.flags)
		buf.WriteString(se.entry.path)
		buf.WriteByte(0) // null terminator

		// pad to 8-byte boundary
		entrySize := buf.Len() - entryStart
		padding := (8 - (entrySize % 8)) % 8
		for i := 0; i < padding; i++ {
			buf.WriteByte(0)
		}
	}

	// checksum (always SHA1 for git index)
	checksum := SHA1Hash.HashBytes(buf.Bytes())
	buf.Write(checksum)

	// write to file
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	_, err := f.Write(buf.Bytes())
	return err
}
