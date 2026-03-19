package repodojo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type RepoOpts struct {
	Hash   HashKind
	IsTest bool
}

type Repo struct {
	opts     RepoOpts
	workPath string
	repoDir  string
}

func InitRepo(workPath string, opts RepoOpts) (*Repo, error) {
	if !filepath.IsAbs(workPath) {
		return nil, errors.New("path must be absolute")
	}

	// create work directory
	if err := os.MkdirAll(workPath, 0755); err != nil {
		return nil, err
	}

	gitDir := filepath.Join(workPath, ".git")

	// check if repo already exists
	if _, err := os.Stat(gitDir); err == nil {
		return nil, errors.New("repo already exists")
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

	repo := &Repo{
		opts:     opts,
		workPath: workPath,
		repoDir:  gitDir,
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
	gitDir := filepath.Join(workPath, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return nil, fmt.Errorf("not a git repository: %s", workPath)
	}
	return &Repo{opts: opts, workPath: workPath, repoDir: gitDir}, nil
}

func (r *Repo) Close() error {
	return nil
}

func (r *Repo) AddConfig(input AddConfigInput) error {
	config, err := r.loadConfig()
	if err != nil {
		return err
	}

	if err := config.Add(input); err != nil {
		return err
	}

	lock, err := NewLockFile(r.repoDir, "config")
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

func (r *Repo) Add(paths []string) error {
	return r.addPaths(paths)
}

func (r *Repo) Commit(metadata CommitMetadata) (string, error) {
	return r.writeCommit(metadata)
}

func (r *Repo) AddTag(input AddTagInput) (string, error) {
	return r.addTag(input)
}
