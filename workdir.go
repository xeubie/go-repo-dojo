package gitgonano

// AddPaths stages the given paths by reading them from the work directory,
// writing blob objects, updating the index, and writing the index file.
func AddPaths(workDir, repoDir string, opts RepoOpts, paths []string) error {
	idx, err := ReadIndex(repoDir, opts.Hash)
	if err != nil {
		return err
	}

	for _, p := range paths {
		parts := SplitPath(p)
		indexPath := JoinPath(parts)
		if err := idx.AddPath(workDir, repoDir, indexPath); err != nil {
			return err
		}
	}

	// write index using lock file
	lock, err := NewLockFile(repoDir, "index")
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
