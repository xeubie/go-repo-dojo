package repomofo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type RepoOpts struct {
	Hash       HashKind
	IsTest     bool
	BufferSize int
	Store      ObjectStore // optional; defaults to FileObjectStore
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
		store = NewFileObjectStore(gitDir, opts)
	}

	repo := &Repo{
		opts:     opts,
		workPath: workPath,
		repoPath: gitDir,
		store:    store,
	}

	// create default branch "master"
	if err := repo.addBranch(AddBranchInput{Name: "master"}); err != nil {
		return nil, err
	}

	// set HEAD to point to refs/heads/master
	if err := repo.replaceHead(RefOrOid{
		IsRef: true,
		Ref:   Ref{Kind: RefHead, Name: "master"},
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
				store = NewFileObjectStore(gitDir, opts)
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

func (r *Repo) AddConfig(input AddConfigInput) error {
	config, err := r.loadConfig()
	if err != nil {
		return err
	}

	if err := config.Add(input); err != nil {
		return err
	}

	lock, err := NewLockFile(r.repoPath, "config")
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

func (r *Repo) RemoveConfig(input RemoveConfigInput) error {
	config, err := r.loadConfig()
	if err != nil {
		return err
	}

	if err := config.Remove(input); err != nil {
		return err
	}

	lock, err := NewLockFile(r.repoPath, "config")
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

func (r *Repo) ListConfig() (*Config, error) {
	return r.loadConfig()
}

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

	lock, err := NewLockFile(r.repoPath, "config")
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

	lock, err := NewLockFile(r.repoPath, "config")
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

func (r *Repo) Add(paths []string) error {
	normalized, err := NormalizePaths(r.workPath, paths)
	if err != nil {
		return err
	}
	return r.addPaths(normalized)
}

func (r *Repo) Unadd(paths []string, opts UnaddOptions) error {
	normalized, err := NormalizePaths(r.workPath, paths)
	if err != nil {
		return err
	}
	return r.unaddPaths(normalized, opts)
}

func (r *Repo) Untrack(paths []string, force, recursive bool) error {
	normalized, err := NormalizePaths(r.workPath, paths)
	if err != nil {
		return err
	}
	return r.removePaths(normalized, RemoveOptions{
		Force:         force,
		Recursive:     recursive,
		UpdateWorkDir: false,
	})
}

func (r *Repo) Remove(paths []string, opts RemoveOptions) error {
	normalized, err := NormalizePaths(r.workPath, paths)
	if err != nil {
		return err
	}
	return r.removePaths(normalized, opts)
}

func (r *Repo) Commit(metadata CommitMetadata) (string, error) {
	return r.writeCommit(metadata)
}

func (r *Repo) Status() (*Status, error) {
	return r.status()
}

func (r *Repo) Restore(path string) error {
	rel, err := RelativePath(r.workPath, path)
	if err != nil {
		return err
	}
	return r.restore(JoinPath(SplitPath(rel)))
}

func (r *Repo) ResetAdd(target RefOrOid) error {
	if target.IsRef {
		return r.replaceHead(target)
	}
	return r.updateHead(target.OID)
}

func (r *Repo) Switch(input SwitchInput) (*SwitchResult, error) {
	return r.switchDir(input)
}

func (r *Repo) Log(startOIDs []string) (*ObjectIterator, error) {
	iter := r.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterCommit})

	if len(startOIDs) == 0 {
		headOID, err := r.ReadHeadRecurMaybe()
		if err != nil {
			return nil, err
		}
		if headOID != "" {
			iter.Include(headOID)
		}
	} else {
		for _, oid := range startOIDs {
			iter.Include(oid)
		}
	}

	return iter, nil
}

func (r *Repo) AddBranch(input AddBranchInput) error {
	return r.addBranch(input)
}

func (r *Repo) RemoveBranch(input RemoveBranchInput) error {
	return r.removeBranch(input)
}

func (r *Repo) ListBranches() (*RefIterator, error) {
	return r.listBranches()
}

func (r *Repo) AddTag(input AddTagInput) (string, error) {
	return r.addTag(input)
}

func (r *Repo) RemoveTag(input RemoveTagInput) error {
	return r.removeTag(input)
}

func (r *Repo) ListTags() (*RefIterator, error) {
	return r.listTags()
}
