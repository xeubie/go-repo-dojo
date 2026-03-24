package repomofo

import (
	"errors"
	"os"
	"path/filepath"
)

type AddTagInput struct {
	Name    string
	Tagger  string
	Message string
}

type RemoveTagInput struct {
	Name string
}

// Creates an annotated tag object pointing at HEAD and returns its OID.
func (repo *Repo) AddTag(input AddTagInput) (Hash, error) {
	if !validateRefName(input.Name) {
		return nil, errors.New("invalid tag name")
	}

	// read HEAD to get the target commit OID
	targetOID, err := repo.ReadHeadRecur()
	if err != nil {
		return nil, err
	}

	// write the tag object
	tagOID, err := repo.writeTag(input, targetOID)
	if err != nil {
		return nil, err
	}

	// write the tag ref
	refPath := "refs/tags/" + input.Name
	if err := repo.writeRef(refPath, OIDValue{OID: tagOID}); err != nil {
		return nil, err
	}

	return tagOID, nil
}

// Deletes a tag ref by name.
func (repo *Repo) RemoveTag(input RemoveTagInput) error {
	tagsDir := filepath.Join(repo.repoPath, "refs", "tags")

	if err := os.Remove(filepath.Join(tagsDir, input.Name)); err != nil {
		return err
	}

	// delete empty parent dirs (for tags with slashes in their name)
	dir := filepath.Dir(filepath.Join(tagsDir, input.Name))
	for dir != tagsDir {
		if err := os.Remove(dir); err != nil {
			break // not empty or doesn't exist
		}
		dir = filepath.Dir(dir)
	}

	return nil
}

// Returns an iterator over all tags.
func (repo *Repo) ListTags() (*RefIterator, error) {
	tagsDir := filepath.Join(repo.repoPath, "refs", "tags")
	return newRefIterator(tagsDir, RefTag)
}
