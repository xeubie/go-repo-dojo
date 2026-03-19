package gitgonano

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
	if err := AddBranch(gitDir, opts, AddBranchInput{Name: "master"}); err != nil {
		return nil, err
	}

	// set HEAD to point to refs/heads/master
	if err := ReplaceHead(gitDir, RefOrOid{
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

func (r *Repo) RepoDir() string  { return r.repoDir }
func (r *Repo) WorkPath() string { return r.workPath }
func (r *Repo) Opts() RepoOpts   { return r.opts }

func (r *Repo) ReadRef(ref Ref) (string, error) {
	refPath := ref.ToPath()
	result, err := ReadRef(r.repoDir, refPath)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return ReadRefRecur(r.repoDir, *result)
}

func (r *Repo) AddConfig(input AddConfigInput) error {
	config, err := LoadConfig(r.repoDir)
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
	return AddPaths(r.workPath, r.repoDir, r.opts, paths)
}

func (r *Repo) Commit(metadata CommitMetadata) (string, error) {
	return WriteCommit(r.repoDir, r.workPath, r.opts, metadata)
}

func (r *Repo) AddTag(input AddTagInput) (string, error) {
	return AddTag(r.repoDir, r.workPath, r.opts, input)
}
