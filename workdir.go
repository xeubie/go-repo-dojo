package repodojo

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Status represents the current status of the working directory and index.
type Status struct {
	Untracked      map[string]bool
	WorkDirModified map[string]bool
	WorkDirDeleted map[string]bool
	IndexAdded     map[string]bool
	IndexModified  map[string]bool
	IndexDeleted   map[string]bool
}

func (repo *Repo) status() (*Status, error) {
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

	// track which index entries are seen in the work dir
	indexSeen := make(map[string]bool)

	// walk work dir
	repo.walkWorkDir(repo.workPath, ".", idx, indexSeen, untracked, workDirModified)

	// entries in index but not seen in work dir are deleted
	for path, entries := range idx.entries {
		if entries[0] != nil && !indexSeen[path] {
			workDirDeleted[path] = true
		}
	}

	// build head tree (flat map of all paths)
	headTree, err := repo.flattenHeadTree()
	if err != nil {
		// no head tree (no commits yet) — all index entries are "added"
		for path, entries := range idx.entries {
			if entries[0] != nil {
				indexAdded[path] = true
			}
		}
		return &Status{
			Untracked:       untracked,
			WorkDirModified: workDirModified,
			WorkDirDeleted:  workDirDeleted,
			IndexAdded:      indexAdded,
			IndexModified:   indexModified,
			IndexDeleted:    indexDeleted,
		}, nil
	}

	// compare index to head tree
	for path, entries := range idx.entries {
		ie := entries[0]
		if ie == nil {
			continue
		}
		if headEntry, ok := headTree[path]; ok {
			if !bytesEqual(ie.oid, headEntry.OID) || ie.mode != headEntry.Mode {
				indexModified[path] = true
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
		Untracked:       untracked,
		WorkDirModified: workDirModified,
		WorkDirDeleted:  workDirDeleted,
		IndexAdded:      indexAdded,
		IndexModified:   indexModified,
		IndexDeleted:    indexDeleted,
	}, nil
}

func (repo *Repo) walkWorkDir(dirPath, relPath string, idx *Index, seen, untracked, modified map[string]bool) bool {
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
			isFile := repo.walkWorkDir(childFull, childRel, idx, seen, untracked, modified)
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

// flattenHeadTree returns a flat map of all file paths in the HEAD tree.
func (repo *Repo) flattenHeadTree() (map[string]TreeEntry, error) {
	headOID, err := repo.ReadHeadRecurMaybe()
	if err != nil || headOID == "" {
		return nil, fmt.Errorf("no HEAD")
	}

	treeOID, err := repo.readCommitTree(headOID)
	if err != nil {
		return nil, err
	}

	result := make(map[string]TreeEntry)
	if err := repo.flattenTree(treeOID, "", result); err != nil {
		return nil, err
	}
	return result, nil
}

func (repo *Repo) flattenTree(treeOID, prefix string, result map[string]TreeEntry) error {
	obj, err := repo.NewObject(treeOID, true)
	if err != nil {
		return err
	}
	defer obj.Close()

	if obj.Tree == nil {
		return nil
	}

	for _, e := range obj.Tree.Entries {
		var path string
		if prefix == "" {
			path = e.Name
		} else {
			path = prefix + "/" + e.Name
		}

		if e.Mode.ObjType() == ModeObjectTypeTree {
			childOID := hex.EncodeToString(e.OID)
			if err := repo.flattenTree(childOID, path, result); err != nil {
				return err
			}
		} else {
			oidCopy := make([]byte, len(e.OID))
			copy(oidCopy, e.OID)
			result[path] = TreeEntry{OID: oidCopy, Mode: e.Mode}
		}
	}
	return nil
}

type UnaddOptions struct {
	Recursive bool
}

type RemoveOptions struct {
	Force        bool
	Recursive    bool
	UpdateWorkDir bool
}

// addPaths stages the given paths by reading them from the work directory,
// writing blob objects, updating the index, and writing the index file.
func (repo *Repo) addPaths(paths []string) error {
	idx, err := repo.readIndex()
	if err != nil {
		return err
	}

	for _, p := range paths {
		parts := SplitPath(p)
		indexPath := JoinPath(parts)
		if err := idx.AddPath(indexPath); err != nil {
			return err
		}
	}

	lock, err := NewLockFile(repo.repoDir, "index")
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
		parts := SplitPath(p)
		indexPath := JoinPath(parts)

		if !opts.Recursive && idx.IsDir(indexPath) {
			return ErrRecursiveOptionRequired
		}

		idx.RemovePath(indexPath, nil)

		// restore entry from HEAD tree if it exists
		if err := repo.restoreTreeEntryToIndex(idx, parts); err != nil {
			return err
		}
	}

	lock, err := NewLockFile(repo.repoDir, "index")
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
func (repo *Repo) removePaths(paths []string, opts RemoveOptions) error {
	idx, err := repo.readIndex()
	if err != nil {
		return err
	}

	removedPaths := make(map[string]bool)

	for _, p := range paths {
		parts := SplitPath(p)
		indexPath := JoinPath(parts)

		// check if path exists in index or on disk
		_, inEntries := idx.entries[indexPath]
		inDir := idx.IsDir(indexPath)
		fullPath := filepath.Join(repo.workPath, indexPath)
		_, statErr := os.Lstat(fullPath)
		existsOnDisk := statErr == nil

		if !inEntries && !inDir && !existsOnDisk {
			return ErrRemoveIndexPathNotFound
		}

		if !opts.Recursive && idx.IsDir(indexPath) {
			return ErrRecursiveOptionRequired
		}

		idx.RemovePath(indexPath, removedPaths)
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

			headOID, headMode := repo.headTreeEntryOIDAndMode(p)

			differsFromHead := false
			if headOID != nil {
				if cleanEntry.mode != headMode || !bytesEqual(cleanEntry.oid, headOID) {
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

	lock, err := NewLockFile(repo.repoDir, "index")
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
func (repo *Repo) restoreTreeEntryToIndex(idx *Index, pathParts []string) error {
	oid, mode, err := repo.lookupTreeEntry(pathParts)
	if err != nil {
		return nil // not found in HEAD tree, nothing to restore
	}

	oidBytes, err := hex.DecodeString(oid)
	if err != nil {
		return err
	}

	indexPath := JoinPath(pathParts)

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

	entry := &IndexEntry{
		mode:     mode,
		oid:      oidBytes,
		fileSize: uint32(obj.Size),
		flags:    uint16(len(indexPath)) & 0xFFF,
		path:     indexPath,
	}
	idx.addEntry(entry)
	return nil
}

// restoreTreeDirToIndex recursively adds all entries from a tree object to the index.
func (repo *Repo) restoreTreeDirToIndex(idx *Index, treeOID string, prefix string) error {
	obj, err := repo.NewObject(treeOID, true)
	if err != nil {
		return err
	}
	defer obj.Close()

	if obj.Tree == nil {
		return nil
	}

	for _, te := range obj.Tree.Entries {
		childPath := JoinPath([]string{prefix, te.Name})

		if te.Mode.ObjType() == ModeObjectTypeTree {
			childOID := hex.EncodeToString(te.OID)
			if err := repo.restoreTreeDirToIndex(idx, childOID, childPath); err != nil {
				return err
			}
		} else {
			childOID := hex.EncodeToString(te.OID)
			childObj, err := repo.NewObject(childOID, false)
			if err != nil {
				return err
			}
			childObj.Close()

			entry := &IndexEntry{
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

// lookupTreeEntry walks the HEAD tree to find the entry at the given path.
// Returns hex OID, mode, and error.
func (repo *Repo) lookupTreeEntry(pathParts []string) (string, Mode, error) {
	headOID, err := repo.ReadHeadRecurMaybe()
	if err != nil || headOID == "" {
		return "", 0, fmt.Errorf("no HEAD")
	}

	treeOID, err := repo.readCommitTree(headOID)
	if err != nil {
		return "", 0, err
	}

	currentTreeOID := treeOID
	for i, part := range pathParts {
		obj, err := repo.NewObject(currentTreeOID, true)
		if err != nil {
			return "", 0, err
		}

		found := false
		if obj.Tree != nil {
			for _, te := range obj.Tree.Entries {
				if te.Name == part {
					oid := hex.EncodeToString(te.OID)
					obj.Close()
					if i == len(pathParts)-1 {
						return oid, te.Mode, nil
					}
					if te.Mode.ObjType() != ModeObjectTypeTree {
						return "", 0, fmt.Errorf("not a tree: %s", part)
					}
					currentTreeOID = oid
					found = true
					break
				}
			}
		}
		if !found {
			obj.Close()
			return "", 0, fmt.Errorf("not found: %s", part)
		}
	}
	return "", 0, fmt.Errorf("not found")
}

// headTreeEntryOIDAndMode returns the OID and mode of a path in the HEAD tree.
// Returns nil OID if not found.
func (repo *Repo) headTreeEntryOIDAndMode(filePath string) ([]byte, Mode) {
	parts := SplitPath(filePath)
	oidHex, mode, err := repo.lookupTreeEntry(parts)
	if err != nil {
		return nil, 0
	}
	oidBytes, err := hex.DecodeString(oidHex)
	if err != nil {
		return nil, 0
	}
	return oidBytes, mode
}

// indexDiffersFromWorkDir checks if the work dir file differs from the index entry.
func (repo *Repo) indexDiffersFromWorkDir(entry *IndexEntry, fullPath string) (bool, error) {
	var content []byte
	var err error

	if entry.mode.ObjType() == ModeObjectTypeSymlink {
		// for symlinks, compare the link target
		target, err := os.Readlink(fullPath)
		if err != nil {
			return true, nil
		}
		content = []byte(target)
	} else {
		content, err = os.ReadFile(fullPath)
		if err != nil {
			return true, nil
		}
	}

	oid, err := repo.writeBlob(content)
	if err != nil {
		return false, err
	}

	return !bytesEqual(entry.oid, oid), nil
}

// restore restores a file or directory in the work dir from the HEAD tree.
func (repo *Repo) restore(path string) error {
	parts := SplitPath(path)
	oidHex, mode, err := repo.lookupTreeEntry(parts)
	if err != nil {
		return fmt.Errorf("object not found: %s", path)
	}

	oidBytes, err := hex.DecodeString(oidHex)
	if err != nil {
		return err
	}

	return repo.objectToFile(JoinPath(parts), TreeEntry{OID: oidBytes, Mode: mode})
}

// TreeChange represents a change between two tree entries.
type TreeChange struct {
	Old *TreeEntry // nil means added
	New *TreeEntry // nil means removed
}

// treeDiff computes changes between two commit OIDs.
// Either oid may be "" to represent an empty tree.
func (repo *Repo) treeDiff(oldCommitOID, newCommitOID string) (map[string]TreeChange, error) {
	changes := make(map[string]TreeChange)

	var oldTreeOID, newTreeOID string
	var err error
	if oldCommitOID != "" {
		oldTreeOID, err = repo.readCommitTree(oldCommitOID)
		if err != nil {
			return nil, err
		}
	}
	if newCommitOID != "" {
		newTreeOID, err = repo.readCommitTree(newCommitOID)
		if err != nil {
			return nil, err
		}
	}

	return repo.compareTreesRecursive(oldTreeOID, newTreeOID, "", changes)
}

func (repo *Repo) compareTreesRecursive(oldTreeOID, newTreeOID, prefix string, changes map[string]TreeChange) (map[string]TreeChange, error) {
	oldEntries, err := repo.loadTreeEntries(oldTreeOID)
	if err != nil {
		return nil, err
	}
	newEntries, err := repo.loadTreeEntries(newTreeOID)
	if err != nil {
		return nil, err
	}

	// deletions and edits
	for name, oldEntry := range oldEntries {
		path := name
		if prefix != "" {
			path = prefix + "/" + name
		}
		oe := oldEntry
		if newEntry, ok := newEntries[name]; ok {
			ne := newEntry
			if !bytesEqual(oe.OID, ne.OID) || oe.Mode != ne.Mode {
				oldIsTree := oe.Mode.ObjType() == ModeObjectTypeTree
				newIsTree := ne.Mode.ObjType() == ModeObjectTypeTree
				oldSub := ""
				if oldIsTree {
					oldSub = hex.EncodeToString(oe.OID)
				}
				newSub := ""
				if newIsTree {
					newSub = hex.EncodeToString(ne.OID)
				}
				if oldIsTree || newIsTree {
					if _, err := repo.compareTreesRecursive(oldSub, newSub, path, changes); err != nil {
						return nil, err
					}
				}
				if !oldIsTree || !newIsTree {
					var oldP, newP *TreeEntry
					if !oldIsTree {
						oldP = &oe
					}
					if !newIsTree {
						newP = &ne
					}
					changes[path] = TreeChange{Old: oldP, New: newP}
				}
			}
		} else {
			if oe.Mode.ObjType() == ModeObjectTypeTree {
				sub := hex.EncodeToString(oe.OID)
				if _, err := repo.compareTreesRecursive(sub, "", path, changes); err != nil {
					return nil, err
				}
			} else {
				changes[path] = TreeChange{Old: &oe, New: nil}
			}
		}
	}

	// additions
	for name, newEntry := range newEntries {
		if _, ok := oldEntries[name]; ok {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "/" + name
		}
		ne := newEntry
		if ne.Mode.ObjType() == ModeObjectTypeTree {
			sub := hex.EncodeToString(ne.OID)
			if _, err := repo.compareTreesRecursive("", sub, path, changes); err != nil {
				return nil, err
			}
		} else {
			changes[path] = TreeChange{Old: nil, New: &ne}
		}
	}

	return changes, nil
}

func (repo *Repo) loadTreeEntries(treeOID string) (map[string]TreeEntry, error) {
	result := make(map[string]TreeEntry)
	if treeOID == "" {
		return result, nil
	}
	obj, err := repo.NewObject(treeOID, true)
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	if obj.Tree == nil {
		return result, nil
	}
	for _, e := range obj.Tree.Entries {
		oidCopy := make([]byte, len(e.OID))
		copy(oidCopy, e.OID)
		result[e.Name] = TreeEntry{OID: oidCopy, Mode: e.Mode}
	}
	return result, nil
}

// SwitchKind differentiates between switch and reset.
type SwitchKind int

const (
	SwitchKindSwitch SwitchKind = iota
	SwitchKindReset
)

// SwitchInput holds the parameters for a switch/reset operation.
type SwitchInput struct {
	Kind         SwitchKind
	Target       RefOrOid // the branch or OID to switch to
	UpdateWorkDir bool
	Force        bool
}

// SwitchConflict holds paths that conflict with the switch.
type SwitchConflict struct {
	StaleFiles            []string
	StaleDirs             []string
	UntrackedOverwritten  []string
	UntrackedRemoved      []string
}

// SwitchResult is the outcome of a switch operation.
type SwitchResult struct {
	Success  bool
	Conflict *SwitchConflict
}

// switchDir switches the working directory, index, and HEAD to a new target.
func (repo *Repo) switchDir(input SwitchInput) (*SwitchResult, error) {
	// resolve current OID
	currentOID, _ := repo.ReadHeadRecurMaybe()

	// resolve target OID
	targetOID, err := repo.readRefRecur(input.Target)
	if err != nil {
		return nil, ErrInvalidSwitchTarget
	}
	if targetOID == "" {
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
	if !input.Force {
		conflict := repo.checkSwitchConflicts(changes, idx)
		if conflict != nil {
			return &SwitchResult{Conflict: conflict}, nil
		}
	}

	// apply removals
	for path, change := range changes {
		if change.New == nil {
			// remove from work dir
			if input.UpdateWorkDir {
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
			// remove from index
			idx.RemovePath(path, nil)
		}
	}

	// apply additions and edits
	for path, change := range changes {
		if change.New != nil {
			// write file to work dir
			if input.UpdateWorkDir {
				if err := repo.objectToFile(path, *change.New); err != nil {
					return nil, err
				}
			}
			// update index
			entry := &IndexEntry{
				mode:  change.New.Mode,
				oid:   change.New.OID,
				flags: uint16(len(path)) & 0xFFF,
				path:  path,
			}
			idx.addEntry(entry)
		}
	}

	// write index
	lock, err := NewLockFile(repo.repoDir, "index")
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
	return &SwitchResult{Success: true}, nil
}

// checkSwitchConflicts checks if switching would overwrite uncommitted changes.
func (repo *Repo) checkSwitchConflicts(changes map[string]TreeChange, idx *Index) *SwitchConflict {
	var staleFiles, staleDirs, untrackedOverwritten, untrackedRemoved []string

	for path, change := range changes {
		entries, inIndex := idx.entries[path]
		var indexEntry *IndexEntry
		if inIndex {
			indexEntry = entries[0]
		}

		// check if both old and new tree differ from the index
		oldDiffersFromIndex := treeEntryDiffersFromIndex(change.Old, indexEntry)
		newDiffersFromIndex := treeEntryDiffersFromIndex(change.New, indexEntry)
		if oldDiffersFromIndex && newDiffersFromIndex {
			staleFiles = append(staleFiles, path)
			continue
		}

		fullPath := filepath.Join(repo.workPath, path)
		info, statErr := os.Lstat(fullPath)
		if statErr != nil {
			// file doesn't exist — check if an untracked parent is blocking
			if repo.hasUntrackedParent(path, idx) {
				if indexEntry != nil {
					staleFiles = append(staleFiles, path)
				} else if change.New != nil {
					untrackedOverwritten = append(untrackedOverwritten, path)
				} else {
					untrackedRemoved = append(untrackedRemoved, path)
				}
			}
			continue
		}

		if info.IsDir() {
			// directory where a file is expected — check if it has untracked descendants
			hasUntracked := repo.hasUntrackedDescendant(fullPath, idx)
			if hasUntracked {
				if indexEntry != nil {
					staleFiles = append(staleFiles, path)
				} else {
					staleDirs = append(staleDirs, path)
				}
			}
		} else {
			// check if work dir differs from index
			if indexEntry != nil {
				differs, err := repo.indexDiffersFromWorkDir(indexEntry, fullPath)
				if err == nil && differs {
					staleFiles = append(staleFiles, path)
				}
			} else {
				// untracked file would be overwritten or removed
				if change.New != nil {
					untrackedOverwritten = append(untrackedOverwritten, path)
				} else {
					untrackedRemoved = append(untrackedRemoved, path)
				}
			}
		}
	}

	if len(staleFiles) > 0 || len(staleDirs) > 0 || len(untrackedOverwritten) > 0 || len(untrackedRemoved) > 0 {
		return &SwitchConflict{
			StaleFiles:           staleFiles,
			StaleDirs:            staleDirs,
			UntrackedOverwritten: untrackedOverwritten,
			UntrackedRemoved:     untrackedRemoved,
		}
	}
	return nil
}

func treeEntryDiffersFromIndex(te *TreeEntry, ie *IndexEntry) bool {
	if te == nil && ie == nil {
		return false
	}
	if te == nil || ie == nil {
		return true
	}
	return te.Mode != ie.mode || !bytesEqual(te.OID, ie.oid)
}

// hasUntrackedParent checks if any parent of the path exists as an untracked file in the work dir.
func (repo *Repo) hasUntrackedParent(path string, idx *Index) bool {
	parts := SplitPath(path)
	for i := 1; i < len(parts); i++ {
		parentPath := JoinPath(parts[:i])
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

// hasUntrackedDescendant checks if a directory has any file that is not in the index.
func (repo *Repo) hasUntrackedDescendant(dirPath string, idx *Index) bool {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return false
	}
	for _, e := range entries {
		fullPath := filepath.Join(dirPath, e.Name())
		relPath, err := filepath.Rel(repo.workPath, fullPath)
		if err != nil {
			continue
		}
		relPath = filepath.ToSlash(relPath)

		if e.IsDir() {
			if repo.hasUntrackedDescendant(fullPath, idx) {
				return true
			}
		} else {
			if _, ok := idx.entries[relPath]; !ok {
				return true
			}
		}
	}
	return false
}

// objectToFile writes a blob object to the work dir.
func (repo *Repo) objectToFile(path string, te TreeEntry) error {
	oidHex := hex.EncodeToString(te.OID)

	fullPath := filepath.Join(repo.workPath, path)
	parentDir := filepath.Dir(fullPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return err
	}

	if te.Mode.ObjType() == ModeObjectTypeTree {
		obj, err := repo.NewObject(oidHex, true)
		if err != nil {
			return err
		}
		defer obj.Close()
		if obj.Tree != nil {
			for _, e := range obj.Tree.Entries {
				childPath := path + "/" + e.Name
				oidCopy := make([]byte, len(e.OID))
				copy(oidCopy, e.OID)
				if err := repo.objectToFile(childPath, TreeEntry{OID: oidCopy, Mode: e.Mode}); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if te.Mode.ObjType() == ModeObjectTypeSymlink {
		// read the blob to get the symlink target
		obj, err := repo.NewObject(oidHex, false)
		if err != nil {
			return err
		}
		defer obj.Close()
		data := make([]byte, obj.Size)
		n, err := obj.reader.Read(data)
		if err != nil && err.Error() != "EOF" {
			return err
		}
		data = data[:n]
		// remove existing file/symlink if present
		os.Remove(fullPath)
		return os.Symlink(string(data), fullPath)
	}

	// regular file
	obj, err := repo.NewObject(oidHex, false)
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
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
