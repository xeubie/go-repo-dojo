package gitgonano

import (
	"errors"
	"os"
	"path/filepath"
)

type AddBranchInput struct {
	Name string
}

func ValidateBranchName(name string) bool {
	return ValidateRefName(name) && name != "HEAD"
}

func AddBranch(repoDir string, opts RepoOpts, input AddBranchInput) error {
	if !ValidateBranchName(input.Name) {
		return errors.New("invalid branch name")
	}

	// check if branch already exists
	exists, err := RefExists(repoDir, Ref{Kind: RefHead, Name: input.Name})
	if err != nil {
		return err
	}
	if exists {
		return errors.New("branch already exists")
	}

	// ensure refs/heads directory exists
	headsDir := filepath.Join(repoDir, "refs", "heads")
	if err := os.MkdirAll(headsDir, 0755); err != nil {
		return err
	}

	// get HEAD OID (might not exist for new repos)
	oidHex, _ := ReadHeadRecurMaybe(repoDir)

	if oidHex != "" {
		// create the branch file with the current HEAD oid
		lock, err := NewLockFile(headsDir, input.Name)
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
