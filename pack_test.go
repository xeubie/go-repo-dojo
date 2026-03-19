package repodojo

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

	repo, err := InitRepo(workPath, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()

	// first commit
	if err := os.WriteFile(filepath.Join(workPath, "hello.txt"), []byte("hello, world!"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workPath, "README"), []byte("My cool project"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := repo.Add([]string{"hello.txt", "README"}); err != nil {
		t.Fatal(err)
	}
	commitOID1, err := repo.Commit(CommitMetadata{Message: "let there be light"})
	if err != nil {
		t.Fatal(err)
	}

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
	if err := repo.Add([]string{"LICENSE", "CHANGELOG", "hello.txt"}); err != nil {
		t.Fatal(err)
	}
	commitOID2, err := repo.Commit(CommitMetadata{Message: "add license"})
	if err != nil {
		t.Fatal(err)
	}

	// write a pack file using PackWriter
	headOID, err := repo.ReadHeadRecur()
	if err != nil {
		t.Fatal(err)
	}

	objIter := repo.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterAll})
	defer objIter.Close()
	objIter.Include(headOID)

	packWriter, err := repo.NewPackWriter(objIter)
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

	// write one byte at a time (like the zig test)
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

	// read the pack back and find both commits by OID
	for _, tc := range []struct {
		oid     string
		message string
	}{
		{commitOID1, "let there be light"},
		{commitOID2, "add license"},
	} {
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
			por, err := iter.Next(repo, nil)
			if err != nil {
				t.Fatal(err)
			}
			if por == nil {
				break
			}

			header := por.Header()
			computedOID := hashPackObject(opts.Hash, header, por)
			por.Close()

			if computedOID == tc.oid {
				found = true
				// verify the commit message by re-reading from the pack
				pr2, err := NewPackReaderFromFile(packFilePath)
				if err != nil {
					t.Fatal(err)
				}
				iter2, err := NewPackIterator(pr2)
				if err != nil {
					pr2.Close()
					t.Fatal(err)
				}
				msg := findCommitMessage(t, iter2, repo, tc.oid)
				pr2.Close()
				if msg != tc.message {
					t.Fatalf("expected message %q, got %q", tc.message, msg)
				}
				break
			}
		}
		pr.Close()

		if !found {
			t.Fatalf("object %s not found in pack", tc.oid)
		}
	}
}

// hashPackObject computes the OID of a pack object by hashing "type size\0" + content.
func hashPackObject(hashKind HashKind, header ObjectHeader, r io.Reader) string {
	headerStr := fmt.Sprintf("%s %d\x00", header.Kind.Name(), header.Size)
	hasher := hashKind.NewHasher()
	hasher.Write([]byte(headerStr))
	io.Copy(hasher, r)
	return hex.EncodeToString(hasher.Sum(nil))
}

// findCommitMessage iterates a pack to find a commit by OID and returns its message.
func findCommitMessage(t *testing.T, iter *PackIterator, repo *Repo, targetOID string) string {
	t.Helper()
	for {
		por, err := iter.Next(repo, nil)
		if err != nil {
			t.Fatal(err)
		}
		if por == nil {
			t.Fatalf("commit %s not found in pack", targetOID)
		}

		header := por.Header()
		if header.Kind != ObjectKindCommit {
			por.Close()
			continue
		}

		// read content and hash to check OID
		content, err := io.ReadAll(por)
		por.Close()
		if err != nil {
			t.Fatal(err)
		}

		headerStr := fmt.Sprintf("commit %d\x00", header.Size)
		hasher := repo.opts.Hash.NewHasher()
		hasher.Write([]byte(headerStr))
		hasher.Write(content)
		computedOID := hex.EncodeToString(hasher.Sum(nil))

		if computedOID == targetOID {
			// parse message from raw commit content
			return parseMessageFromCommitBytes(string(content))
		}
	}
}

func parseMessageFromCommitBytes(content string) string {
	// message starts after the first blank line
	idx := 0
	for {
		nl := indexOf(content[idx:], '\n')
		if nl < 0 {
			return ""
		}
		if nl == 0 {
			// blank line found
			return content[idx+1:]
		}
		idx += nl + 1
	}
}

func indexOf(s string, c byte) int {
	for i := range s {
		if s[i] == c {
			return i
		}
	}
	return -1
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

	objIter := clientRepo.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterAll})
	defer objIter.Close()
	objIter.Include(commit2)

	pw, err := clientRepo.NewPackWriter(objIter)
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

	if err := serverRepo.CopyFromPackIterator(packIter); err != nil {
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

	if err := repo.CopyFromPackIterator(packIter); err != nil {
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

	if err := repo.CopyFromPackIterator(packIter); err != nil {
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
