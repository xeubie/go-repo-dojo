package repomofo

import (
	"errors"
	"os"
	"path/filepath"
)

var (
	ErrBranchAlreadyExists       = errors.New("branch already exists")
	ErrCannotDeleteCurrentBranch = errors.New("cannot delete current branch")
)

type AddBranchInput struct {
	Name string
}

type RemoveBranchInput struct {
	Name string
}

func validateBranchName(name string) bool {
	return validateRefName(name) && name != "HEAD"
}

func (repo *Repo) addBranch(input AddBranchInput) error {
	if !validateBranchName(input.Name) {
		return errors.New("invalid branch name")
	}

	exists, err := repo.refExists(Ref{Kind: RefHead, Name: input.Name})
	if err != nil {
		return err
	}
	if exists {
		return ErrBranchAlreadyExists
	}

	branchPath := filepath.Join(repo.repoPath, "refs", "heads", input.Name)
	if err := os.MkdirAll(filepath.Dir(branchPath), 0755); err != nil {
		return err
	}
	headsDir := filepath.Join(repo.repoPath, "refs", "heads")

	oidHex, _ := repo.ReadHeadRecurMaybe()

	if oidHex != "" {
		lock, err := newLockFile(headsDir, input.Name)
		if err != nil {
			return err
		}
		defer lock.Close()

		if _, err := lock.File.WriteString(oidHex + "\n"); err != nil {
			return err
		}
		lock.Success = true
	}

	return nil
}

func (repo *Repo) removeBranch(input RemoveBranchInput) error {
	// don't allow current branch to be deleted
	currentRef, err := repo.readRef("HEAD")
	if err == nil && currentRef != nil && currentRef.IsRef {
		if currentRef.Ref.Kind == RefHead && currentRef.Ref.Name == input.Name {
			return ErrCannotDeleteCurrentBranch
		}
	}

	headsDir := filepath.Join(repo.repoPath, "refs", "heads")

	if err := os.Remove(filepath.Join(headsDir, input.Name)); err != nil {
		return err
	}

	// delete empty parent dirs (for branches with slashes)
	dir := filepath.Dir(filepath.Join(headsDir, input.Name))
	for dir != headsDir {
		if err := os.Remove(dir); err != nil {
			break
		}
		dir = filepath.Dir(dir)
	}

	return nil
}

func (repo *Repo) listBranches() (*RefIterator, error) {
	headsDir := filepath.Join(repo.repoPath, "refs", "heads")
	return newRefIterator(headsDir, RefHead)
}

// HeadResult represents what HEAD points to.
type HeadResult struct {
	IsRef bool
	Ref   Ref    // valid when IsRef == true
	OID   string // valid when IsRef == false
}

// Returns what HEAD currently points to — either a branch ref or a detached OID.
func (repo *Repo) Head() (*HeadResult, error) {
	result, err := repo.readRef("HEAD")
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("HEAD not found")
	}
	if result.IsRef {
		return &HeadResult{IsRef: true, Ref: result.Ref}, nil
	}
	return &HeadResult{OID: result.OID}, nil
}
