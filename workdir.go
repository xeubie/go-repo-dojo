package repomofo

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// MergeConflictStatus tracks which conflict slots are present for a path.
// Slot 1 = base, slot 2 = target (ours), slot 3 = source (theirs).
type MergeConflictStatus struct {
	Base   bool
	Target bool
	Source bool
}

// Status represents the current status of the working directory and index.
type Status struct {
	Untracked           map[string]bool
	WorkDirModified     map[string]bool
	WorkDirDeleted      map[string]bool
	IndexAdded          map[string]bool
	IndexModified       map[string]bool
	IndexDeleted        map[string]bool
	UnresolvedConflicts map[string]MergeConflictStatus
	ResolvedConflicts   map[string]TreeEntry
}

// Returns the working directory and index status relative to HEAD.
func (repo *Repo) Status() (*Status, error) {
	idx, err := repo.readIndex()
	if err != nil {
		return nil, err
	}

	untracked := make(map[string]bool)
	workDirModified := make(map[string]bool)
	workDirDeleted := make(map[string]bool)
	indexAdded := make(map[string]bool)
	indexModified := make(map[string]bool)
	indexDeleted := make(map[string]bool)
	unresolvedConflicts := make(map[string]MergeConflictStatus)
	resolvedConflicts := make(map[string]TreeEntry)

	// track which index entries are seen in the work dir
	indexSeen := make(map[string]bool)

	// walk work dir
	repo.addEntries(repo.workPath, ".", idx, indexSeen, untracked, workDirModified)

	// entries in index but not seen in work dir are deleted
	for path, entries := range idx.entries {
		if entries[0] != nil && !indexSeen[path] {
			workDirDeleted[path] = true
		}
	}

	// build head tree (flat map of all paths)
	headTree, headErr := repo.flattenHeadTree()

	// read merge source tree if a merge is in progress
	var mergeSourceTree map[string]TreeEntry
	if mergeSourceOID, err := readAnyMergeHead(repo); err == nil && mergeSourceOID != nil {
		mergeSourceTreeOID, err := repo.readCommitTree(mergeSourceOID)
		if err == nil {
			mergeSourceTree = make(map[string]TreeEntry)
			repo.flattenTree(mergeSourceTreeOID, "", mergeSourceTree) //nolint:errcheck
		}
	}

	if headErr != nil {
		// no head tree (no commits yet) — all non-conflict index entries are "added"
		for path, entries := range idx.entries {
			if entries[0] != nil {
				indexAdded[path] = true
			} else {
				unresolvedConflicts[path] = MergeConflictStatus{
					Base:   entries[1] != nil,
					Target: entries[2] != nil,
					Source: entries[3] != nil,
				}
			}
		}
		return &Status{
			Untracked:           untracked,
			WorkDirModified:     workDirModified,
			WorkDirDeleted:      workDirDeleted,
			IndexAdded:          indexAdded,
			IndexModified:       indexModified,
			IndexDeleted:        indexDeleted,
			UnresolvedConflicts: unresolvedConflicts,
			ResolvedConflicts:   resolvedConflicts,
		}, nil
	}

	// compare index to head tree
	for path, entries := range idx.entries {
		ie := entries[0]
		if ie == nil {
			// conflict entry (slot 0 is empty)
			unresolvedConflicts[path] = MergeConflictStatus{
				Base:   entries[1] != nil,
				Target: entries[2] != nil,
				Source: entries[3] != nil,
			}
			continue
		}

		if headEntry, ok := headTree[path]; ok {
			if !HashEqual(ie.oid, headEntry.OID) || ie.mode != headEntry.Mode {
				indexModified[path] = true
			} else if mergeSourceTree != nil {
				// head entry matches index — check if it was a resolved conflict
				if mergeSourceEntry, ok := mergeSourceTree[path]; ok {
					if !HashEqual(headEntry.OID, mergeSourceEntry.OID) || headEntry.Mode != mergeSourceEntry.Mode {
						// merge source differs from head; check index doesn't match merge source either
						if !HashEqual(ie.oid, mergeSourceEntry.OID) || ie.mode != mergeSourceEntry.Mode {
							resolvedConflicts[path] = mergeSourceEntry
						}
					}
				}
			}
		} else {
			indexAdded[path] = true
		}
	}

	// entries in head tree but not in index are deleted
	for path := range headTree {
		if _, ok := idx.entries[path]; !ok {
			indexDeleted[path] = true
		}
	}

	return &Status{
		Untracked:           untracked,
		WorkDirModified:     workDirModified,
		WorkDirDeleted:      workDirDeleted,
		IndexAdded:          indexAdded,
		IndexModified:       indexModified,
		IndexDeleted:        indexDeleted,
		UnresolvedConflicts: unresolvedConflicts,
		ResolvedConflicts:   resolvedConflicts,
	}, nil
}

func (repo *Repo) addEntries(dirPath, relPath string, idx *index, seen, untracked, modified map[string]bool) bool {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return false
	}

	containsFile := false
	isTrackedDir := relPath == "." || idx.IsDir(relPath) || func() bool {
		_, ok := idx.entries[relPath]
		return ok
	}()

	var childUntracked []string

	for _, e := range entries {
		name := e.Name()
		if name == ".git" {
			continue
		}

		childFull := filepath.Join(dirPath, name)
		var childRel string
		if relPath == "." {
			childRel = name
		} else {
			childRel = relPath + "/" + name
		}

		if e.IsDir() {
			isFile := repo.addEntries(childFull, childRel, idx, seen, untracked, modified)
			containsFile = containsFile || isFile
			if isFile && !isTrackedDir {
				break
			}
		} else {
			containsFile = true

			if entries, ok := idx.entries[childRel]; ok && entries[0] != nil {
				seen[childRel] = true
				differs, err := repo.indexDiffersFromWorkDir(entries[0], childFull)
				if err == nil && differs {
					modified[childRel] = true
				}
			} else {
				if isTrackedDir {
					untracked[childRel] = true
				} else {
					childUntracked = append(childUntracked, childRel)
				}
			}
		}
	}

	if !isTrackedDir {
		if containsFile {
			untracked[relPath] = true
		}
	} else {
		for _, p := range childUntracked {
			untracked[p] = true
		}
	}

	return containsFile
}

type UnaddOptions struct {
	Recursive bool
}

type UntrackOptions struct {
	Force     bool
	Recursive bool
}

type RemoveOptions struct {
	Force     bool
	Recursive bool
}

type removeOptions struct {
	Force         bool
	Recursive     bool
	UpdateWorkDir bool
}

// indexDiffersFromWorkDir checks if the work dir file differs from the index entry.
func (repo *Repo) indexDiffersFromWorkDir(entry *indexEntry, fullPath string) (bool, error) {
	info, err := os.Lstat(fullPath)
	if err != nil {
		return true, nil
	}

	if entry.mode.ObjType() == ModeObjectTypeSymlink {
		if uint32(info.Size()) != entry.fileSize {
			return true, nil
		}
		target, err := os.Readlink(fullPath)
		if err != nil {
			return true, nil
		}
		oid, err := repo.writeBlob([]byte(target))
		if err != nil {
			return false, err
		}
		return !HashEqual(entry.oid, oid), nil
	}

	// size check — avoids opening the file when size differs
	if uint32(info.Size()) != entry.fileSize {
		return true, nil
	}

	// executable bit check
	wantExec := entry.mode.UnixPerm() == 0o755
	isExec := info.Mode().Perm()&0o111 != 0
	if wantExec != isExec {
		return true, nil
	}

	// mtime check — if mtime is unchanged, assume content is unchanged
	mtimeSecs := uint32(info.ModTime().Unix())
	mtimeNsecs := uint32(info.ModTime().Nanosecond())
	if mtimeSecs == entry.mtimeSecs && mtimeNsecs == entry.mtimeNsecs {
		return false, nil
	}

	// mtime changed — hash to confirm
	f, err := os.Open(fullPath)
	if err != nil {
		return true, nil
	}
	oid, err := repo.writeBlobFromReader(f, uint64(info.Size()))
	f.Close()
	if err != nil {
		return false, err
	}
	return !HashEqual(entry.oid, oid), nil
}

// addPaths stages the given paths by reading them from the work directory,
// writing blob objects, updating the index, and writing the index file.
func (repo *Repo) addPaths(paths []string) error {
	idx, err := repo.readIndex()
	if err != nil {
		return err
	}

	for _, p := range paths {
		if err := idx.AddOrRemovePath(p, indexActionAdd, nil); err != nil {
			return err
		}
	}

	lock, err := newLockFile(repo.repoPath, "index")
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := idx.Write(lock.File); err != nil {
		return err
	}

	lock.Success = true
	return nil
}

// unaddPaths removes the given paths from the index and restores
// the HEAD tree entries if they exist (like `git reset HEAD`).
func (repo *Repo) unaddPaths(paths []string, opts UnaddOptions) error {
	idx, err := repo.readIndex()
	if err != nil {
		return err
	}

	for _, p := range paths {
		if !opts.Recursive && idx.IsDir(p) {
			return ErrRecursiveOptionRequired
		}

		if err := idx.AddOrRemovePath(p, indexActionRm, nil); err != nil {
			return err
		}

		// restore entry from HEAD tree if it exists
		parts := splitPath(p)
		if err := repo.restoreTreeEntryToIndex(idx, parts); err != nil {
			return err
		}
	}

	lock, err := newLockFile(repo.repoPath, "index")
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := idx.Write(lock.File); err != nil {
		return err
	}

	lock.Success = true
	return nil
}

// removePaths removes paths from the index and optionally from the work dir.
func (repo *Repo) removePaths(paths []string, opts removeOptions) error {
	idx, err := repo.readIndex()
	if err != nil {
		return err
	}

	removedPaths := make(map[string]bool)

	for _, p := range paths {
		if !opts.Recursive && idx.IsDir(p) {
			return ErrRecursiveOptionRequired
		}

		if err := idx.AddOrRemovePath(p, indexActionRm, removedPaths); err != nil {
			return err
		}
	}

	// safety check
	if !opts.Force {
		cleanIdx, err := repo.readIndex()
		if err != nil {
			return err
		}

		for p := range removedPaths {
			fullPath := filepath.Join(repo.workPath, p)
			_, statErr := os.Lstat(fullPath)
			if statErr != nil {
				continue // file doesn't exist in work dir, safe to remove
			}

			cleanEntries, hasClean := cleanIdx.entries[p]
			if !hasClean {
				continue
			}
			cleanEntry := cleanEntries[0]
			if cleanEntry == nil {
				continue
			}

			headOID, headMode := repo.headTreeEntry(p)

			differsFromHead := false
			if headOID != nil {
				if cleanEntry.mode != headMode || !HashEqual(cleanEntry.oid, headOID) {
					differsFromHead = true
				}
			}

			differsFromWorkDir, err := repo.indexDiffersFromWorkDir(cleanEntry, fullPath)
			if err != nil {
				return err
			}

			if differsFromHead && differsFromWorkDir {
				return ErrCannotRemoveFileWithStagedAndUnstagedChanges
			} else if differsFromHead && opts.UpdateWorkDir {
				return ErrCannotRemoveFileWithStagedChanges
			} else if differsFromWorkDir && opts.UpdateWorkDir {
				return ErrCannotRemoveFileWithUnstagedChanges
			}
		}
	}

	// remove files from work dir
	if opts.UpdateWorkDir {
		for p := range removedPaths {
			fullPath := filepath.Join(repo.workPath, p)
			os.Remove(fullPath)

			// remove empty parent directories
			dir := filepath.Dir(fullPath)
			for dir != repo.workPath {
				if err := os.Remove(dir); err != nil {
					break
				}
				dir = filepath.Dir(dir)
			}
		}
	}

	lock, err := newLockFile(repo.repoPath, "index")
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := idx.Write(lock.File); err != nil {
		return err
	}

	lock.Success = true
	return nil
}

// restoreTreeEntryToIndex looks up a path in the HEAD tree and adds it
// back to the index if found.
func (repo *Repo) restoreTreeEntryToIndex(idx *index, pathParts []string) error {
	oid, mode, err := repo.pathToTreeEntry(pathParts)
	if err != nil {
		return nil // not found in HEAD tree, nothing to restore
	}

	indexPath := joinPath(pathParts)

	if mode.ObjType() == ModeObjectTypeTree {
		// it's a directory in the tree — recurse into it
		return repo.restoreTreeDirToIndex(idx, oid, indexPath)
	}

	// read object to get the file size
	obj, err := repo.NewObject(oid, false)
	if err != nil {
		return err
	}
	obj.Close()

	entry := &indexEntry{
		mode:     mode,
		oid:      oid,
		fileSize: uint32(obj.Size),
		flags:    uint16(len(indexPath)) & 0xFFF,
		path:     indexPath,
	}
	idx.addEntry(entry)
	return nil
}

// restoreTreeDirToIndex recursively adds all entries from a tree object to the index.
func (repo *Repo) restoreTreeDirToIndex(idx *index, treeOID Hash, prefix string) error {
	obj, err := repo.NewObject(treeOID, true)
	if err != nil {
		return err
	}
	defer obj.Close()

	if obj.Tree == nil {
		return nil
	}

	for _, te := range obj.Tree.Entries {
		childPath := joinPath([]string{prefix, te.Name})

		if te.Mode.ObjType() == ModeObjectTypeTree {
			if err := repo.restoreTreeDirToIndex(idx, te.OID, childPath); err != nil {
				return err
			}
		} else {
			childObj, err := repo.NewObject(te.OID, false)
			if err != nil {
				return err
			}
			childObj.Close()

			entry := &indexEntry{
				mode:     te.Mode,
				oid:      te.OID,
				fileSize: uint32(childObj.Size),
				flags:    uint16(len(childPath)) & 0xFFF,
				path:     childPath,
			}
			idx.addEntry(entry)
		}
	}
	return nil
}

// objectToFile writes a blob object to the work dir.
func (repo *Repo) objectToFile(path string, te TreeEntry) error {
	fullPath := filepath.Join(repo.workPath, path)
	parentDir := filepath.Dir(fullPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return err
	}

	switch te.Mode.ObjType() {
	case ModeObjectTypeRegularFile:
		obj, err := repo.NewObject(te.OID, false)
		if err != nil {
			return err
		}
		defer obj.Close()

		perm := os.FileMode(0644)
		if te.Mode.UnixPerm() == 0o755 {
			perm = 0755
		}

		f, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
		if err != nil {
			return err
		}
		defer f.Close()

		buf := make([]byte, 8192)
		for {
			n, readErr := obj.reader.Read(buf)
			if n > 0 {
				if _, err := f.Write(buf[:n]); err != nil {
					return err
				}
			}
			if readErr != nil {
				break
			}
		}

		return nil

	case ModeObjectTypeTree:
		obj, err := repo.NewObject(te.OID, true)
		if err != nil {
			return err
		}
		defer obj.Close()
		if obj.Tree != nil {
			for _, e := range obj.Tree.Entries {
				childPath := path + "/" + e.Name
				if err := repo.objectToFile(childPath, TreeEntry{OID: e.OID, Mode: e.Mode}); err != nil {
					return err
				}
			}
		}
		return nil

	case ModeObjectTypeSymlink:
		if runtime.GOOS == "windows" {
			// Windows requires special privileges for symlinks,
			// so write the symlink target as a regular file instead.
			return repo.objectToFile(path, TreeEntry{OID: te.OID, Mode: Mode(0o100644)})
		} else {
			os.Remove(fullPath)
			obj, err := repo.NewObject(te.OID, false)
			if err != nil {
				return err
			}
			defer obj.Close()
			data := make([]byte, obj.Size)
			n, err := obj.reader.Read(data)
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			data = data[:n]
			return os.Symlink(string(data), fullPath)
		}

	case ModeObjectTypeGitlink:
		return fmt.Errorf("submodules not supported")

	default:
		return fmt.Errorf("unknown object type: %d", te.Mode.ObjType())
	}
}

func treeEntryDiffersFromIndex(te *TreeEntry, ie *indexEntry) bool {
	if te == nil && ie == nil {
		return false
	}
	if te == nil || ie == nil {
		return true
	}
	return te.Mode != ie.mode || !HashEqual(te.OID, ie.oid)
}

// untrackedFile returns true if the given file or one of its descendants (if a dir)
// isn't tracked by the index, so it cannot be safely removed by checkout.
func (repo *Repo) untrackedFile(fullPath, relPath string, idx *index) bool {
	info, err := os.Lstat(fullPath)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		_, ok := idx.entries[relPath]
		return !ok
	}
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return false
	}
	for _, e := range entries {
		childFull := filepath.Join(fullPath, e.Name())
		childRel := relPath + "/" + e.Name()
		if repo.untrackedFile(childFull, childRel, idx) {
			return true
		}
	}
	return false
}

// untrackedParent checks if any parent of the path exists as an untracked file in the work dir.
func (repo *Repo) untrackedParent(path string, idx *index) bool {
	parts := splitPath(path)
	for i := 1; i < len(parts); i++ {
		parentPath := joinPath(parts[:i])
		fullParent := filepath.Join(repo.workPath, parentPath)
		info, err := os.Lstat(fullParent)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			// a file exists where a directory is expected
			if _, ok := idx.entries[parentPath]; !ok {
				return true
			}
		}
	}
	return false
}

// SwitchKind differentiates between switch and reset.
type SwitchKind int

const (
	SwitchKindSwitch SwitchKind = iota
	SwitchKindReset
)

type ResetInput struct {
	Target RefOrOid
	Force  bool
}

type SwitchInput struct {
	Target RefOrOid
	Force  bool
}

type switchInput struct {
	Kind          SwitchKind
	Target        RefOrOid
	UpdateWorkDir bool
	Force         bool
}

// SwitchConflict holds paths that conflict with the switch.
type SwitchConflict struct {
	StaleFiles           []string
	StaleDirs            []string
	UntrackedOverwritten []string
	UntrackedRemoved     []string
}

// SwitchResult is the outcome of a switch operation.
type SwitchResult interface {
	switchResult()
}

// SwitchSuccess indicates a successful switch.
type SwitchSuccess struct{}

func (SwitchSuccess) switchResult()   {}
func (*SwitchConflict) switchResult() {}

type SwitchOutput struct {
	Result SwitchResult
}

// migrate applies tree diff changes to the index and optionally the work dir.
// If result is non-nil, conflicts are checked and recorded rather than applied.
func (repo *Repo) migrate(changes map[string]TreeChange, idx *index, updateWorkDir bool, result *SwitchConflict) error {
	addFiles := make(map[string]TreeEntry)
	removeFiles := make(map[string]bool)

	for path, change := range changes {
		if change.Old == nil {
			if change.New != nil {
				addFiles[path] = *change.New
			}
		} else if change.New == nil {
			removeFiles[path] = true
		} else {
			if change.New != nil {
				addFiles[path] = *change.New
			}
		}

		// check for conflicts
		if result != nil {
			entries, inIndex := idx.entries[path]
			var indexEntry *indexEntry
			if inIndex {
				indexEntry = entries[0]
			}

			oldDiffersFromIndex := treeEntryDiffersFromIndex(change.Old, indexEntry)
			newDiffersFromIndex := treeEntryDiffersFromIndex(change.New, indexEntry)
			if oldDiffersFromIndex && newDiffersFromIndex {
				result.StaleFiles = append(result.StaleFiles, path)
				continue
			}

			fullPath := filepath.Join(repo.workPath, path)
			info, statErr := os.Lstat(fullPath)
			if statErr != nil {
				if repo.untrackedParent(path, idx) {
					if indexEntry != nil {
						result.StaleFiles = append(result.StaleFiles, path)
					} else if change.New != nil {
						result.UntrackedOverwritten = append(result.UntrackedOverwritten, path)
					} else {
						result.UntrackedRemoved = append(result.UntrackedRemoved, path)
					}
				}
				continue
			}

			if info.IsDir() {
				if repo.untrackedFile(fullPath, path, idx) {
					if indexEntry != nil {
						result.StaleFiles = append(result.StaleFiles, path)
					} else {
						result.StaleDirs = append(result.StaleDirs, path)
					}
				}
			} else {
				if indexEntry != nil {
					differs, err := repo.indexDiffersFromWorkDir(indexEntry, fullPath)
					if err == nil && differs {
						result.StaleFiles = append(result.StaleFiles, path)
					}
				} else {
					if change.New != nil {
						result.UntrackedOverwritten = append(result.UntrackedOverwritten, path)
					} else {
						result.UntrackedRemoved = append(result.UntrackedRemoved, path)
					}
				}
			}
		}
	}

	// if there are any unresolved conflicts, either exit with an error
	// or (if -f is being used) replace the conflicting index entries
	for path, entries := range idx.entries {
		// ignore non-conflict entries
		if entries[0] != nil {
			continue
			// we can't switch if there is an unresolved merge conflict
		} else if result != nil {
			result.StaleFiles = append(result.StaleFiles, path)
			// if we are using -f, and the conflicting file isn't being removed,
			// just add it so the index is updated (making it non-conflicting)
			// and the work dir is (optionally) updated
		} else if _, inAddFiles := addFiles[path]; !removeFiles[path] && !inAddFiles {
			conflictEntry := entries[2]
			if conflictEntry == nil {
				conflictEntry = entries[3]
			}
			if conflictEntry == nil {
				return fmt.Errorf("null entry for conflict at %s", path)
			}
			addFiles[path] = TreeEntry{OID: conflictEntry.oid, Mode: conflictEntry.mode}
		}
	}

	if result != nil && result.hasConflict() {
		return nil
	}

	// apply removals
	for path := range removeFiles {
		if updateWorkDir {
			fullPath := filepath.Join(repo.workPath, path)
			os.Remove(fullPath)
			dir := filepath.Dir(fullPath)
			for dir != repo.workPath {
				if err := os.Remove(dir); err != nil {
					break
				}
				dir = filepath.Dir(dir)
			}
		}
		idx.RemovePath(path, nil)
	}

	// apply additions and edits
	for path, te := range addFiles {
		if updateWorkDir {
			if err := repo.objectToFile(path, te); err != nil {
				return err
			}
		}
		if err := idx.AddPath(path, &te); err != nil {
			return err
		}
	}

	return nil
}

// switchDir switches HEAD, the index, and optionally the working directory to a new target.
func (repo *Repo) switchDir(input switchInput) (*SwitchOutput, error) {
	// resolve current OID
	currentOID, _ := repo.ReadHeadRecurMaybe()

	// resolve target OID
	targetOID, err := repo.readRefRecur(input.Target)
	if err != nil {
		return nil, ErrInvalidSwitchTarget
	}
	if targetOID == nil {
		return nil, ErrInvalidSwitchTarget
	}

	// compute tree diff
	changes, err := repo.treeDiff(currentOID, targetOID)
	if err != nil {
		return nil, err
	}

	// read the index
	idx, err := repo.readIndex()
	if err != nil {
		return nil, err
	}

	// check for conflicts (unless force)
	var conflict *SwitchConflict
	if !input.Force {
		conflict = &SwitchConflict{}
	}

	if err := repo.migrate(changes, idx, input.UpdateWorkDir, conflict); err != nil {
		return nil, err
	}

	if conflict != nil && conflict.hasConflict() {
		return &SwitchOutput{Result: conflict}, nil
	}

	// write index
	lock, err := newLockFile(repo.repoPath, "index")
	if err != nil {
		return nil, err
	}
	defer lock.Close()

	if err := idx.Write(lock.File); err != nil {
		return nil, err
	}

	// update HEAD
	switch input.Kind {
	case SwitchKindSwitch:
		if err := repo.replaceHead(input.Target); err != nil {
			return nil, err
		}
	case SwitchKindReset:
		if err := repo.updateHead(targetOID); err != nil {
			return nil, err
		}
	}

	lock.Success = true

	// if -f, we need to clean up stored merge state as well
	if input.Force {
		removeMergeState(repo)
	}

	return &SwitchOutput{Result: SwitchSuccess{}}, nil
}

func (c *SwitchConflict) hasConflict() bool {
	return len(c.StaleFiles) > 0 || len(c.StaleDirs) > 0 || len(c.UntrackedOverwritten) > 0 || len(c.UntrackedRemoved) > 0
}

// restore restores a file or directory in the work dir from the HEAD tree.
func (repo *Repo) restore(path string) error {
	parts := splitPath(path)
	oid, mode, err := repo.pathToTreeEntry(parts)
	if err != nil {
		return fmt.Errorf("object not found: %s", path)
	}

	return repo.objectToFile(joinPath(parts), TreeEntry{OID: oid, Mode: mode})
}
