package repodojo

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

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

	entry := &IndexEntry{
		mode:     mode,
		oid:      oidBytes,
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
			entry := &IndexEntry{
				mode:  te.Mode,
				oid:   te.OID,
				flags: uint16(len(childPath)) & 0xFFF,
				path:  childPath,
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
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return true, nil
	}

	oid, err := repo.writeBlob(content)
	if err != nil {
		return false, err
	}

	return !bytesEqual(entry.oid, oid), nil
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
