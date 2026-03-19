package gitgonano

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateAndReadPack(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	tempDir := filepath.Join(cwd, "temp-test-create-and-read-pack")
	os.RemoveAll(tempDir)
	defer os.RemoveAll(tempDir)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		t.Fatal(err)
	}

	workPath := filepath.Join(tempDir, "repo")
	opts := RepoOpts{Hash: SHA1Hash, IsTest: true}

	// init repo using git CLI
	cmd := exec.Command("git", "init", workPath)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s\n%s", err, out)
	}

	gitDir := filepath.Join(workPath, ".git")
	gitEnv := append(os.Environ(),
		"GIT_DIR="+gitDir,
		"GIT_WORK_TREE="+workPath,
		"GIT_AUTHOR_NAME=radarroark",
		"GIT_AUTHOR_EMAIL=radarroark@radar.roark",
		"GIT_AUTHOR_DATE=1970-01-01T00:00:00+0000",
		"GIT_COMMITTER_NAME=radarroark",
		"GIT_COMMITTER_EMAIL=radarroark@radar.roark",
		"GIT_COMMITTER_DATE=1970-01-01T00:00:00+0000",
		"GIT_CONFIG_GLOBAL=/dev/null",
	)

	// first commit
	if err := os.WriteFile(filepath.Join(workPath, "hello.txt"), []byte("hello, world!"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workPath, "README"), []byte("My cool project"), 0644); err != nil {
		t.Fatal(err)
	}

	gitRun := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Env = gitEnv
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	gitRun("add", "hello.txt", "README")
	gitRun("commit", "-m", "let there be light")
	commitOID1 := gitRun("rev-parse", "HEAD")

	// second commit
	if err := os.WriteFile(filepath.Join(workPath, "LICENSE"), []byte("do whatever you want"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workPath, "CHANGELOG"), []byte("cha-cha-cha-changes"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workPath, "hello.txt"), []byte("goodbye, world!"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "LICENSE", "CHANGELOG", "hello.txt")
	gitRun("commit", "-m", "add license")
	commitOID2 := gitRun("rev-parse", "HEAD")

	// create pack file via git
	packInput := commitOID1 + "\n" + commitOID2 + "\n"
	packCmd := exec.Command("git", "pack-objects", "--revs", filepath.Join(gitDir, "objects", "pack", "pack"))
	packCmd.Env = gitEnv
	packCmd.Stdin = strings.NewReader(packInput)
	if out, err := packCmd.CombinedOutput(); err != nil {
		t.Fatalf("git pack-objects: %s\n%s", err, out)
	}

	// verify pack files exist
	packDirPath := filepath.Join(gitDir, "objects", "pack")
	entries, err := os.ReadDir(packDirPath)
	if err != nil {
		t.Fatal(err)
	}
	fileCount := 0
	for _, e := range entries {
		if e.Type().IsRegular() {
			fileCount++
		}
	}
	if fileCount < 2 {
		t.Fatalf("expected at least 2 pack files, got %d", fileCount)
	}

	// delete loose commit objects
	for _, oid := range []string{commitOID1, commitOID2} {
		loosePath := filepath.Join(gitDir, "objects", oid[:2], oid[2:])
		os.Remove(loosePath)
	}

	// read pack objects by OID
	for _, tc := range []struct {
		oid     string
		message string
	}{
		{commitOID1, "let there be light"},
		{commitOID2, "add license"},
	} {
		obj, err := NewObject(gitDir, opts.Hash, tc.oid, true)
		if err != nil {
			t.Fatalf("NewObject(%s): %v", tc.oid, err)
		}
		if obj.Commit == nil {
			t.Fatalf("expected commit object for %s", tc.oid)
		}
		if obj.Commit.Message != tc.message {
			t.Fatalf("expected message %q, got %q", tc.message, obj.Commit.Message)
		}
		obj.Close()
	}

	// write a pack file using PackWriter and read it back
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatal(err)
		}
		defer repo.Close()

		headOID, err := ReadHeadRecur(gitDir)
		if err != nil {
			t.Fatal(err)
		}

		objIter := NewObjectIterator(gitDir, opts.Hash, ObjectIteratorOptions{Kind: ObjectIterAll})
		defer objIter.Close()
		objIter.Include(headOID)

		packWriter, err := NewPackWriter(opts.Hash, objIter)
		if err != nil {
			t.Fatal(err)
		}
		if packWriter == nil {
			t.Fatal("PackWriter is nil")
		}
		defer packWriter.Close()

		packFilePath := filepath.Join(tempDir, "test.pack")
		packFile, err := os.Create(packFilePath)
		if err != nil {
			t.Fatal(err)
		}

		var buf [1]byte
		for {
			n, err := packWriter.Read(buf[:])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := packFile.Write(buf[:n]); err != nil {
				t.Fatal(err)
			}
			if n < len(buf) {
				break
			}
		}
		packFile.Close()

		// read the written pack back using initWithoutIndex style search
		for _, oid := range []string{commitOID1, commitOID2} {
			pr, err := NewPackReaderFromFile(packFilePath)
			if err != nil {
				t.Fatal(err)
			}

			iter, err := NewPackIterator(pr)
			if err != nil {
				pr.Close()
				t.Fatal(err)
			}

			found := false
			for {
				por, err := iter.Next(gitDir, opts.Hash, nil)
				if err != nil {
					t.Fatal(err)
				}
				if por == nil {
					break
				}

				header := por.Header()
				headerStr := fmt.Sprintf("%s %d\x00", header.Kind.Name(), header.Size)
				hasher := opts.Hash.NewHasher()
				hasher.Write([]byte(headerStr))

				data, err := readAllFromPackObj(por)
				por.Close()
				if err != nil {
					t.Fatal(err)
				}
				hasher.Write(data)
				computedOID := hex.EncodeToString(hasher.Sum(nil))

				if computedOID == oid {
					found = true
					break
				}
			}
			pr.Close()

			if !found {
				t.Fatalf("object %s not found in written pack", oid)
			}
		}
	}
}

func readAllFromPackObj(por *PackObjectReader) ([]byte, error) {
	var buf [4096]byte
	var result []byte
	for {
		n, err := por.Read(buf[:])
		if n > 0 {
			result = append(result, buf[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return result, err
		}
		if n == 0 {
			break
		}
	}
	return result, nil
}

func TestWritePackFile(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	tempDir := filepath.Join(cwd, "temp-test-write-pack-file")
	os.RemoveAll(tempDir)
	defer os.RemoveAll(tempDir)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		t.Fatal(err)
	}

	clientPath := filepath.Join(tempDir, "client")
	opts := RepoOpts{Hash: SHA1Hash, IsTest: true}

	clientRepo, err := InitRepo(clientPath, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer clientRepo.Close()

	// create some files
	if err := os.WriteFile(filepath.Join(clientPath, "file1.txt"), []byte("content of file 1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clientPath, "file2.txt"), []byte("content of file 2"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := clientRepo.Add([]string{"file1.txt", "file2.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := clientRepo.Commit(CommitMetadata{Message: "let there be light"}); err != nil {
		t.Fatal(err)
	}

	// modify files
	if err := os.WriteFile(filepath.Join(clientPath, "file1.txt"), []byte("EDITcontent of file 1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clientPath, "file2.txt"), []byte("EDITcontent of file 2"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := clientRepo.Add([]string{"file1.txt", "file2.txt"}); err != nil {
		t.Fatal(err)
	}
	commit2, err := clientRepo.Commit(CommitMetadata{Message: "more stuff"})
	if err != nil {
		t.Fatal(err)
	}

	// write pack file
	packFilePath := filepath.Join(tempDir, "test.pack")
	packFile, err := os.Create(packFilePath)
	if err != nil {
		t.Fatal(err)
	}

	objIter := NewObjectIterator(clientRepo.repoDir, opts.Hash, ObjectIteratorOptions{Kind: ObjectIterAll})
	defer objIter.Close()
	objIter.Include(commit2)

	pw, err := NewPackWriter(opts.Hash, objIter)
	if err != nil {
		t.Fatal(err)
	}
	if pw == nil {
		t.Fatal("PackWriter is nil")
	}
	defer pw.Close()

	var readBuf [4096]byte
	for {
		n, err := pw.Read(readBuf[:])
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		if _, err := packFile.Write(readBuf[:n]); err != nil {
			t.Fatal(err)
		}
	}
	packFile.Close()

	// read the pack file into a fresh repo
	serverPath := filepath.Join(tempDir, "server")
	serverRepo, err := InitRepo(serverPath, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer serverRepo.Close()

	pr, err := NewPackReaderFromFile(packFilePath)
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()

	packIter, err := NewPackIterator(pr)
	if err != nil {
		t.Fatal(err)
	}

	if err := CopyFromPackIterator(serverRepo.repoDir, opts.Hash, packIter); err != nil {
		t.Fatal(err)
	}
}

func TestIteratePackFromFile(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	tempDir := filepath.Join(cwd, "temp-test-iterate-file-packreader")
	os.RemoveAll(tempDir)
	defer os.RemoveAll(tempDir)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		t.Fatal(err)
	}

	workPath := filepath.Join(tempDir, "repo")
	opts := RepoOpts{Hash: SHA1Hash, IsTest: true}

	repo, err := InitRepo(workPath, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	packPath := filepath.Join(cwd, "testdata", "pack-b7f085e431fc05b0bca3d5c306dc148d7bbed2f4.pack")

	pr, err := NewPackReaderFromFile(packPath)
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()

	packIter, err := NewPackIterator(pr)
	if err != nil {
		t.Fatal(err)
	}

	if err := CopyFromPackIterator(repo.repoDir, opts.Hash, packIter); err != nil {
		t.Fatal(err)
	}
}

func TestIteratePackFromStream(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	tempDir := filepath.Join(cwd, "temp-test-iterate-stream-packreader")
	os.RemoveAll(tempDir)
	defer os.RemoveAll(tempDir)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		t.Fatal(err)
	}

	workPath := filepath.Join(tempDir, "repo")
	opts := RepoOpts{Hash: SHA1Hash, IsTest: true}

	repo, err := InitRepo(workPath, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	packPath := filepath.Join(cwd, "testdata", "pack-b7f085e431fc05b0bca3d5c306dc148d7bbed2f4.pack")
	file, err := os.Open(packPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	bufReader := bufio.NewReader(file)
	countingReader := NewCountingReader(bufReader)
	pr := NewPackReaderFromStream(countingReader)
	defer pr.Close()

	packIter, err := NewPackIterator(pr)
	if err != nil {
		t.Fatal(err)
	}

	if err := CopyFromPackIterator(repo.repoDir, opts.Hash, packIter); err != nil {
		t.Fatal(err)
	}
}

func TestReadPackedRefs(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	tempDir := filepath.Join(cwd, "temp-test-read-packed-refs")
	os.RemoveAll(tempDir)
	defer os.RemoveAll(tempDir)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		t.Fatal(err)
	}

	workPath := filepath.Join(tempDir, "repo")
	opts := RepoOpts{Hash: SHA1Hash, IsTest: true}

	repo, err := InitRepo(workPath, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	packedRefsContent := `# pack-refs with: peeled fully-peeled sorted
5246e54744f4e1824ca280e6a2630a87959d7cf4 refs/remotes/origin/master
1ea47a890400815b24a0073f110a41530322a44f refs/remotes/sync/chunk
5246e54744f4e1824ca280e6a2630a87959d7cf4 refs/remotes/sync/master
1f6190c71bd33b37cfd885491889a0410f849f5b refs/remotes/sync/zig-0.14.0
`
	if err := os.WriteFile(filepath.Join(repo.repoDir, "packed-refs"), []byte(packedRefsContent), 0644); err != nil {
		t.Fatal(err)
	}

	oid, err := repo.ReadRef(Ref{Kind: RefRemote, RemoteName: "sync", Name: "master"})
	if err != nil {
		t.Fatal(err)
	}
	if oid != "5246e54744f4e1824ca280e6a2630a87959d7cf4" {
		t.Fatalf("expected 5246e54744f4e1824ca280e6a2630a87959d7cf4, got %s", oid)
	}

	oid2, err := repo.ReadRef(Ref{Kind: RefRemote, RemoteName: "sync", Name: "foo"})
	if err != nil && err != ErrRefNotFound {
		t.Fatal(err)
	}
	if oid2 != "" {
		t.Fatalf("expected empty oid for non-existent ref, got %s", oid2)
	}
}
