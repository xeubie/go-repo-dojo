package repomofo

import (
	"os"
	"path/filepath"
	"testing"
)

func addFile(t *testing.T, repo *Repo, path, content string) {
	t.Helper()
	fullPath := filepath.Join(repo.workPath, path)
	parentDir := filepath.Dir(fullPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		t.Fatalf("mkdir %s failed: %v", parentDir, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("write %s failed: %v", path, err)
	}
	if err := repo.Add([]string{path}); err != nil {
		t.Fatalf("add %s failed: %v", path, err)
	}
}

func TestSimple(t *testing.T) {
	t.Run("FileObjectStore", func(t *testing.T) {
		Simple(t, nil)
	})
	t.Run("MemoryObjectStore", func(t *testing.T) {
		Simple(t, NewMemoryObjectStore(SHA1Hash))
	})
}

func Simple(t *testing.T, store ObjectStore) {
	t.Helper()
	tempDir := t.TempDir()
	workPath := filepath.Join(tempDir, "repo")

	opts := RepoOpts{
		Hash:   SHA1Hash,
		IsTest: true,
		Store:  store,
	}

	_, err := InitRepo(workPath, opts)
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}

	repo, err := OpenRepo(workPath, opts)
	if err != nil {
		t.Fatalf("open repo failed: %v", err)
	}

	addFile(t, repo, "README.md", "Hello, world!")
	commitA, err := repo.Commit(CommitMetadata{Message: "a"})
	if err != nil {
		t.Fatalf("commit a failed: %v", err)
	}

	addFile(t, repo, "README.md", "Goodbye, world!")
	commitB, err := repo.Commit(CommitMetadata{Message: "b"})
	if err != nil {
		t.Fatalf("commit b failed: %v", err)
	}

	err = repo.Remove([]string{"README.md"}, RemoveOptions{UpdateWorkDir: true})
	if err != nil {
		t.Fatalf("remove README.md failed: %v", err)
	}
	commitC, err := repo.Commit(CommitMetadata{Message: "c"})
	if err != nil {
		t.Fatalf("commit c failed: %v", err)
	}

	// can't add path that is outside repo
	err = repo.Add([]string{"../README.md"})
	if err != ErrPathIsOutsideRepo {
		t.Fatalf("expected ErrPathIsOutsideRepo, got %v", err)
	}

	// commits that haven't changed content are an error
	_, err = repo.Commit(CommitMetadata{Message: "d"})
	if err == nil {
		t.Fatal("expected empty commit error")
	}

	// assert that all commits have been found in the log
	{
		oidSet := map[string]bool{
			commitA: true,
			commitB: true,
			commitC: true,
		}

		iter, err := repo.Log(nil)
		if err != nil {
			t.Fatalf("log failed: %v", err)
		}
		for {
			rawObj, err := iter.Next()
			if err != nil {
				t.Fatalf("log next failed: %v", err)
			}
			if rawObj == nil {
				break
			}
			delete(oidSet, rawObj.OID)
			rawObj.Close()
		}
		if len(oidSet) != 0 {
			t.Fatalf("not all commits found in log, remaining: %v", oidSet)
		}
	}

	// reset-dir to commit b
	{
		result, err := repo.Switch(SwitchInput{
			Kind:          SwitchKindReset,
			Target:        RefOrOid{OID: commitB},
			UpdateWorkDir: true,
		})
		if err != nil {
			t.Fatalf("reset-dir to commit b failed: %v", err)
		}
		if !result.Success {
			t.Fatal("reset-dir to commit b was not successful")
		}

		content, err := os.ReadFile(filepath.Join(workPath, "README.md"))
		if err != nil {
			t.Fatalf("read README.md after reset to b failed: %v", err)
		}
		if string(content) != "Goodbye, world!" {
			t.Fatalf("README.md = %q, want %q", string(content), "Goodbye, world!")
		}
	}

	// reset-dir to commit a
	{
		result, err := repo.Switch(SwitchInput{
			Kind:          SwitchKindReset,
			Target:        RefOrOid{OID: commitA},
			UpdateWorkDir: true,
		})
		if err != nil {
			t.Fatalf("reset-dir to commit a failed: %v", err)
		}
		if !result.Success {
			t.Fatal("reset-dir to commit a was not successful")
		}

		content, err := os.ReadFile(filepath.Join(workPath, "README.md"))
		if err != nil {
			t.Fatalf("read README.md after reset to a failed: %v", err)
		}
		if string(content) != "Hello, world!" {
			t.Fatalf("README.md = %q, want %q", string(content), "Hello, world!")
		}
	}

	// reset-dir to commit c
	{
		result, err := repo.Switch(SwitchInput{
			Kind:          SwitchKindReset,
			Target:        RefOrOid{OID: commitC},
			UpdateWorkDir: true,
		})
		if err != nil {
			t.Fatalf("reset-dir to commit c failed: %v", err)
		}
		if !result.Success {
			t.Fatal("reset-dir to commit c was not successful")
		}

		// README.md should not exist after reset to commit c
		if _, err := os.Stat(filepath.Join(workPath, "README.md")); !os.IsNotExist(err) {
			t.Fatal("README.md should not exist after reset to commit c")
		}
	}

	// add a tag
	_, err = repo.AddTag(AddTagInput{Name: "1.0.0", Message: "hi"})
	if err != nil {
		t.Fatalf("add tag failed: %v", err)
	}

	// reset-dir to the tag
	{
		result, err := repo.Switch(SwitchInput{
			Kind:          SwitchKindReset,
			Target:        RefOrOid{IsRef: true, Ref: Ref{Kind: RefTag, Name: "1.0.0"}},
			UpdateWorkDir: true,
		})
		if err != nil {
			t.Fatalf("reset-dir to tag failed: %v", err)
		}
		if !result.Success {
			t.Fatal("reset-dir to tag was not successful")
		}

		// status works when HEAD points to a tag
		_, err = repo.Status()
		if err != nil {
			t.Fatalf("status after reset to tag failed: %v", err)
		}
	}
}
