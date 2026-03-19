package repodojo

import "errors"

type AddTagInput struct {
	Name    string
	Tagger  string
	Message string
}

func (repo *Repo) addTag(input AddTagInput) (string, error) {
	if !ValidateRefName(input.Name) {
		return "", errors.New("invalid tag name")
	}

	// read HEAD to get the target commit OID
	targetOID, err := repo.ReadHeadRecur()
	if err != nil {
		return "", err
	}

	// write the tag object
	tagOID, err := repo.writeTagObject(input, targetOID)
	if err != nil {
		return "", err
	}

	// write the tag ref
	refPath := "refs/tags/" + input.Name
	if err := repo.writeRef(refPath, RefOrOid{OID: tagOID}); err != nil {
		return "", err
	}

	return tagOID, nil
}
