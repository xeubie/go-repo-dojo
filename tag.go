package gitgonano

import "errors"

type AddTagInput struct {
	Name    string
	Tagger  string
	Message string
}

func AddTag(repoDir, workPath string, opts RepoOpts, input AddTagInput) (string, error) {
	if !ValidateRefName(input.Name) {
		return "", errors.New("invalid tag name")
	}

	// read HEAD to get the target commit OID
	targetOID, err := ReadHeadRecur(repoDir)
	if err != nil {
		return "", err
	}

	// write the tag object
	tagOID, err := WriteTagObject(repoDir, workPath, opts, input, targetOID)
	if err != nil {
		return "", err
	}

	// write the tag ref
	refPath := "refs/tags/" + input.Name
	if err := WriteRef(repoDir, refPath, RefOrOid{OID: tagOID}); err != nil {
		return "", err
	}

	return tagOID, nil
}
