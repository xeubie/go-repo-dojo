package gitgonano

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSignCommitAndTag(t *testing.T) {
	tempDirName := "temp-testnet-sign"

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// clean up any existing temp dir
	tempDir := filepath.Join(cwd, tempDirName)
	os.RemoveAll(tempDir)
	defer os.RemoveAll(tempDir)

	if err := os.MkdirAll(tempDir, 0755); err != nil {
		t.Fatal(err)
	}

	workPath := tempDir

	// init repo
	repo, err := InitRepo(workPath, RepoOpts{Hash: SHA1Hash, IsTest: true})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	// create priv key
	privKeyPath := filepath.Join(tempDir, "key")
	privKey := "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n" +
		"QyNTUxOQAAACCniLPJiaooAWecvOCeAjoJwCSeWxzysvpTNkpYjF22JgAAAJA+7hikPu4Y\n" +
		"pAAAAAtzc2gtZWQyNTUxOQAAACCniLPJiaooAWecvOCeAjoJwCSeWxzysvpTNkpYjF22Jg\n" +
		"AAAEDVlopOMnKt/7by/IA8VZvQXUS/O6VLkixOqnnahUdPCKeIs8mJqigBZ5y84J4COgnA\n" +
		"JJ5bHPKy+lM2SliMXbYmAAAAC3JhZGFyQHJvYXJrAQI=\n" +
		"-----END OPENSSH PRIVATE KEY-----\n"
	if err := os.WriteFile(privKeyPath, []byte(privKey), 0600); err != nil {
		t.Fatal(err)
	}

	// create pub key
	pubKeyPath := filepath.Join(tempDir, "key.pub")
	pubKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKeIs8mJqigBZ5y84J4COgnAJJ5bHPKy+lM2SliMXbYm radar@roark\n"
	if err := os.WriteFile(pubKeyPath, []byte(pubKey), 0600); err != nil {
		t.Fatal(err)
	}

	// add key to config
	if err := repo.AddConfig(AddConfigInput{
		Name:  "user.signingkey",
		Value: pubKeyPath,
	}); err != nil {
		t.Fatal(err)
	}

	// make a commit
	helloPath := filepath.Join(workPath, "hello.txt")
	if err := os.WriteFile(helloPath, []byte("hello, world!"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := repo.Add([]string{"hello.txt"}); err != nil {
		t.Fatal(err)
	}

	commitOID, err := repo.Commit(CommitMetadata{Message: "let there be light"})
	if err != nil {
		t.Fatal(err)
	}

	// add a tag
	tagOID, err := repo.AddTag(AddTagInput{Name: "1.0.0", Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	// TODO: verify the objects contain signatures
	_ = commitOID
	_ = tagOID
}
