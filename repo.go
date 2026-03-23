package repomofo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type RepoOpts struct {
	Hash       HashKind
	IsTest     bool
	BufferSize int
	Store      ObjectStore // optional; defaults to fileObjectStore
}

func (o RepoOpts) bufferSize() int {
	if o.BufferSize <= 0 {
		return 4096
	}
	return o.BufferSize
}

type Repo struct {
	opts     RepoOpts
	workPath string
	repoPath string
	store    ObjectStore
}

func InitRepo(workPath string, opts RepoOpts) (*Repo, error) {
	if !filepath.IsAbs(workPath) {
		return nil, fmt.Errorf("path must be absolute")
	}

	// create work directory
	if err := os.MkdirAll(workPath, 0755); err != nil {
		return nil, err
	}

	gitDir := filepath.Join(workPath, ".git")

	// check if repo already exists
	if _, err := os.Stat(gitDir); err == nil {
		return nil, ErrRepoAlreadyExists
	}

	// create .git directory structure
	for _, dir := range []string{
		gitDir,
		filepath.Join(gitDir, "objects"),
		filepath.Join(gitDir, "objects", "pack"),
		filepath.Join(gitDir, "refs"),
		filepath.Join(gitDir, "refs", "heads"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}

	store := opts.Store
	if store == nil {
		store = newFileObjectStore(gitDir, opts)
	}

	repo := &Repo{
		opts:     opts,
		workPath: workPath,
		repoPath: gitDir,
		store:    store,
	}

	// create default branch "master"
	if err := repo.AddBranch(AddBranchInput{Name: "master"}); err != nil {
		return nil, err
	}

	// set HEAD to point to refs/heads/master
	if err := repo.replaceHead(RefValue{
		Ref: Ref{Kind: RefHead, Name: "master"},
	}); err != nil {
		return nil, err
	}

	return repo, nil
}

func OpenRepo(workPath string, opts RepoOpts) (*Repo, error) {
	dir := workPath
	for {
		gitDir := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			store := opts.Store
			if store == nil {
				store = newFileObjectStore(gitDir, opts)
			}
			return &Repo{opts: opts, workPath: dir, repoPath: gitDir, store: store}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrRepoNotFound
		}
		dir = parent
	}
}

// Adds or updates a configuration entry.
func (r *Repo) AddConfig(input AddConfigInput) error {
	config, err := r.loadConfig()
	if err != nil {
		return err
	}

	if err := config.Add(input); err != nil {
		return err
	}

	lock, err := newLockFile(r.repoPath, "config")
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := config.Write(lock.File); err != nil {
		return err
	}

	lock.Success = true
	return nil
}

// Removes a configuration entry.
func (r *Repo) RemoveConfig(input RemoveConfigInput) error {
	config, err := r.loadConfig()
	if err != nil {
		return err
	}

	if err := config.Remove(input); err != nil {
		return err
	}

	lock, err := newLockFile(r.repoPath, "config")
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := config.Write(lock.File); err != nil {
		return err
	}

	lock.Success = true
	return nil
}

// Returns the full parsed configuration.
func (r *Repo) ListConfig() (*Config, error) {
	return r.loadConfig()
}

// Registers a new remote with the given name and URL.
func (r *Repo) AddRemote(name, url string) error {
	config, err := r.loadConfig()
	if err != nil {
		return err
	}

	if err := config.Add(AddConfigInput{
		Name:  fmt.Sprintf("remote.%s.url", name),
		Value: url,
	}); err != nil {
		return err
	}

	if err := config.Add(AddConfigInput{
		Name:  fmt.Sprintf("remote.%s.fetch", name),
		Value: fmt.Sprintf("+refs/heads/*:refs/remotes/%s/*", name),
	}); err != nil {
		return err
	}

	lock, err := newLockFile(r.repoPath, "config")
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := config.Write(lock.File); err != nil {
		return err
	}

	lock.Success = true
	return nil
}

// Deletes a remote and its fetch config by name.
func (r *Repo) RemoveRemote(name string) error {
	config, err := r.loadConfig()
	if err != nil {
		return err
	}

	if err := config.Remove(RemoveConfigInput{
		Name: fmt.Sprintf("remote.%s.url", name),
	}); err != nil {
		return err
	}

	if err := config.Remove(RemoveConfigInput{
		Name: fmt.Sprintf("remote.%s.fetch", name),
	}); err != nil {
		return err
	}

	lock, err := newLockFile(r.repoPath, "config")
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := config.Write(lock.File); err != nil {
		return err
	}

	lock.Success = true
	return nil
}

// Returns configuration entries for all remotes.
func (r *Repo) ListRemotes() (*Config, error) {
	config, err := r.loadConfig()
	if err != nil {
		return nil, err
	}

	const prefix = "remote."
	result := newConfig()
	for _, sectionName := range config.sectionOrder {
		if strings.HasPrefix(sectionName, prefix) {
			shortName := sectionName[len(prefix):]
			result.sections[shortName] = config.sections[sectionName]
			result.sectionOrder = append(result.sectionOrder, shortName)
		}
	}
	return result, nil
}

// Stages the given file paths in the index.
func (r *Repo) Add(paths []string) error {
	normalized, err := normalizePaths(r.workPath, paths)
	if err != nil {
		return err
	}
	return r.addPaths(normalized)
}

// Unstages the given paths, restoring their index entries from HEAD.
func (r *Repo) Unadd(paths []string, opts UnaddOptions) error {
	normalized, err := normalizePaths(r.workPath, paths)
	if err != nil {
		return err
	}
	return r.unaddPaths(normalized, opts)
}

// Removes the given paths from the index without deleting them from the working directory.
func (r *Repo) Untrack(paths []string, force, recursive bool) error {
	normalized, err := normalizePaths(r.workPath, paths)
	if err != nil {
		return err
	}
	return r.removePaths(normalized, RemoveOptions{
		Force:         force,
		Recursive:     recursive,
		UpdateWorkDir: false,
	})
}

// Removes the given paths from the index and optionally from the working directory.
func (r *Repo) Remove(paths []string, opts RemoveOptions) error {
	normalized, err := normalizePaths(r.workPath, paths)
	if err != nil {
		return err
	}
	return r.removePaths(normalized, opts)
}

// Creates a new commit from the current index and returns its OID.
func (r *Repo) Commit(metadata CommitMetadata) (Hash, error) {
	return r.writeCommit(metadata)
}

// Restores a file in the working directory to its HEAD tree content.
func (r *Repo) Restore(path string) error {
	rel, err := relativePath(r.workPath, path)
	if err != nil {
		return err
	}
	return r.restore(joinPath(splitPath(rel)))
}

// Returns what HEAD currently points to — either a branch ref or a detached OID.
func (r *Repo) Head() (RefOrOid, error) {
	result, err := r.readRef("HEAD")
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("HEAD not found")
	}
	return result, nil
}

// Points HEAD at the given ref or OID without modifying the index or working directory.
func (r *Repo) ResetAdd(target RefOrOid) error {
	switch v := target.(type) {
	case RefValue:
		return r.replaceHead(v)
	case OIDValue:
		return r.updateHead(v.OID)
	default:
	}
	return nil
}

// Switches HEAD, the index, and optionally the working directory to a new target.
func (r *Repo) Switch(input SwitchInput) (*SwitchResult, error) {
	return r.switchDir(input)
}

// Returns an iterator over commits reachable from the given OIDs, or from HEAD if none are given.
func (r *Repo) Log(startOIDs []Hash) (*ObjectIterator, error) {
	iter := r.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterCommit})

	if len(startOIDs) == 0 {
		headOID, err := r.ReadHeadRecurMaybe()
		if err != nil {
			return nil, err
		}
		if headOID != nil {
			iter.Include(headOID)
		}
	} else {
		for _, oid := range startOIDs {
			iter.Include(oid)
		}
	}

	return iter, nil
}

