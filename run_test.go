package repodojo

import (
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRun(t *testing.T) {
	tempDir := t.TempDir()

	runOpts := RunOpts{
		Out: io.Discard,
		Err: io.Discard,
	}

	opts := RepoOpts{
		Hash:   SHA1Hash,
		IsTest: true,
	}

	// init repo
	err := Run(opts, []string{"init", "repo"}, tempDir, runOpts)
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}

	workPath := filepath.Join(tempDir, "repo")

	// verify .git dir exists
	gitDir := filepath.Join(workPath, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		t.Fatalf(".git dir not found: %v", err)
	}
	if !info.IsDir() {
		t.Fatal(".git is not a directory")
	}

	// verify subdirectories
	for _, sub := range []string{"objects", "objects/pack", "refs", "refs/heads"} {
		p := filepath.Join(gitDir, sub)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s not found: %v", sub, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", sub)
		}
	}

	// verify HEAD exists and points to master
	headPath := filepath.Join(gitDir, "HEAD")
	headContent, err := os.ReadFile(headPath)
	if err != nil {
		t.Fatalf("HEAD not found: %v", err)
	}
	expected := "ref: refs/heads/master\n"
	if string(headContent) != expected {
		t.Fatalf("HEAD content = %q, want %q", string(headContent), expected)
	}

	// verify we can open the repo
	repo, err := OpenRepo(workPath, opts)
	if err != nil {
		t.Fatalf("open repo failed: %v", err)
	}
	repo.Close()

	helloTxtContent := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n17\n18\n19"

	// create files
	writeFile(t, workPath, "hello.txt", helloTxtContent)
	writeFile(t, workPath, "README", "My cool project")
	writeFile(t, workPath, "LICENSE", "do whatever you want")
	writeFile(t, workPath, "tests", "testing...")
	writeFile(t, workPath, "run.sh", "#!/bin/sh")

	// create file in a subdirectory
	docsDir := filepath.Join(workPath, "docs")
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatalf("mkdir docs failed: %v", err)
	}
	writeFile(t, docsDir, "design.md", "design stuff")

	// add the files
	err = Run(opts, []string{"add", "."}, workPath, runOpts)
	if err != nil {
		t.Fatalf("add failed: %v", err)
	}

	// make a commit (from the docs subdirectory, like the Zig test)
	docsPath := filepath.Join(workPath, "docs")
	err = Run(opts, []string{"commit", "-m", "first commit"}, docsPath, runOpts)
	if err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	// check that the commit object was created
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		defer repo.Close()

		headOID, err := repo.ReadHeadRecurMaybe()
		if err != nil {
			t.Fatalf("read HEAD failed: %v", err)
		}
		if headOID == "" {
			t.Fatal("HEAD has no OID after commit")
		}

		// verify the loose object file exists
		objDir := filepath.Join(gitDir, "objects", headOID[:2])
		objPath := filepath.Join(objDir, headOID[2:])
		if _, err := os.Stat(objPath); err != nil {
			t.Fatalf("commit object not found at %s: %v", objPath, err)
		}

		// read the commit and verify its message
		obj, err := repo.NewObject(headOID, true)
		if err != nil {
			t.Fatalf("read commit object failed: %v", err)
		}
		defer obj.Close()

		if obj.Kind != ObjectKindCommit {
			t.Fatalf("expected commit object, got %s", obj.Kind.Name())
		}
		if obj.Commit.Message != "first commit" {
			t.Fatalf("commit message = %q, want %q", obj.Commit.Message, "first commit")
		}
	}

	// make sure we are hashing files the same way git does
	{
		readmeContent, err := os.ReadFile(filepath.Join(workPath, "README"))
		if err != nil {
			t.Fatalf("read README failed: %v", err)
		}

		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		defer repo.Close()

		oidBytes, err := repo.writeBlob(readmeContent)
		if err != nil {
			t.Fatalf("hash blob failed: %v", err)
		}
		oidHex := hex.EncodeToString(oidBytes)

		// look up README in the committed tree to compare
		headOID, _ := repo.ReadHeadRecurMaybe()
		treeOID, err := repo.readCommitTree(headOID)
		if err != nil {
			t.Fatalf("read commit tree failed: %v", err)
		}
		treeObj, err := repo.NewObject(treeOID, true)
		if err != nil {
			t.Fatalf("read tree failed: %v", err)
		}
		defer treeObj.Close()

		found := false
		for _, entry := range treeObj.Tree.Entries {
			if entry.Name == "README" {
				entryOID := hex.EncodeToString(entry.OID)
				if entryOID != oidHex {
					t.Fatalf("README OID mismatch: tree has %s, hashed %s", entryOID, oidHex)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatal("README not found in committed tree")
		}
	}

	// get HEAD contents (commit1)
	var commit1 string
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		commit1, err = repo.ReadHeadRecurMaybe()
		repo.Close()
		if err != nil {
			t.Fatalf("read HEAD failed: %v", err)
		}
		if commit1 == "" {
			t.Fatal("commit1 is empty")
		}
	}

	newHelloTxtContent := "1\n2\n3\n4\n5.0\n6\n7\n8\n9.0\n10.0\n11\n12\n13\n14\n15.0\n16\n17\n18\n19"

	// make another commit
	{
		// change a file
		writeFile(t, workPath, "hello.txt", newHelloTxtContent)

		// replace a file with a directory
		if err := os.Remove(filepath.Join(workPath, "tests")); err != nil {
			t.Fatalf("delete tests failed: %v", err)
		}
		testsDir := filepath.Join(workPath, "tests")
		if err := os.MkdirAll(testsDir, 0755); err != nil {
			t.Fatalf("mkdir tests failed: %v", err)
		}
		writeFile(t, testsDir, "main_test.zig", "")

		// make a few dirs
		srcZigDir := filepath.Join(workPath, "src", "zig")
		if err := os.MkdirAll(srcZigDir, 0755); err != nil {
			t.Fatalf("mkdir src/zig failed: %v", err)
		}

		// make a file in the dir
		writeFile(t, srcZigDir, "main.zig", "pub fn main() !void {}")

		// make file in a nested dir
		oneTwoDir := filepath.Join(workPath, "one", "two")
		if err := os.MkdirAll(oneTwoDir, 0755); err != nil {
			t.Fatalf("mkdir one/two failed: %v", err)
		}
		writeFile(t, oneTwoDir, "three.txt", "one, two, three!")

		// make run.sh executable
		if err := os.Chmod(filepath.Join(workPath, "run.sh"), 0755); err != nil {
			t.Fatalf("chmod run.sh failed: %v", err)
		}

		// make symlink
		if err := os.Symlink("one/two/three.txt", filepath.Join(workPath, "three.txt")); err != nil {
			t.Fatalf("symlink failed: %v", err)
		}

		// delete a file
		if err := os.Remove(filepath.Join(workPath, "LICENSE")); err != nil {
			t.Fatalf("delete LICENSE failed: %v", err)
		}
		err = Run(opts, []string{"add", "LICENSE"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add LICENSE (deleted) failed: %v", err)
		}

		// delete a file and dir
		if err := os.RemoveAll(filepath.Join(workPath, "docs")); err != nil {
			t.Fatalf("delete docs failed: %v", err)
		}
		err = Run(opts, []string{"add", "docs/design.md"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add docs/design.md (deleted) failed: %v", err)
		}

		// add new and modified files
		err = Run(opts, []string{"add", "hello.txt", "run.sh", "src/zig/main.zig"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add modified files failed: %v", err)
		}

		// add the remaining files
		err = Run(opts, []string{"add", "."}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add remaining failed: %v", err)
		}

		// make another commit
		err = Run(opts, []string{"commit", "-m", "second commit"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("second commit failed: %v", err)
		}
	}

	// verify second commit
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		defer repo.Close()

		commit2, err := repo.ReadHeadRecurMaybe()
		if err != nil {
			t.Fatalf("read HEAD failed: %v", err)
		}
		if commit2 == "" {
			t.Fatal("commit2 is empty")
		}
		if commit2 == commit1 {
			t.Fatal("commit2 should differ from commit1")
		}

		obj, err := repo.NewObject(commit2, true)
		if err != nil {
			t.Fatalf("read commit2 failed: %v", err)
		}
		defer obj.Close()

		if obj.Commit.Message != "second commit" {
			t.Fatalf("commit2 message = %q, want %q", obj.Commit.Message, "second commit")
		}
		if len(obj.Commit.ParentOIDs) != 1 || obj.Commit.ParentOIDs[0] != commit1 {
			t.Fatalf("commit2 parent = %v, want [%s]", obj.Commit.ParentOIDs, commit1)
		}
	}

	// try to switch to first commit after making conflicting change
	{
		// make a new file (and add it to the index) that conflicts with one from commit1
		{
			writeFile(t, workPath, "LICENSE", "different license")
			err = Run(opts, []string{"add", "LICENSE"}, workPath, runOpts)
			if err != nil {
				t.Fatalf("add LICENSE failed: %v", err)
			}

			// check out commit1 and make sure the conflict is found
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			result, err := repo.Switch(SwitchInput{
				Kind:          SwitchKindSwitch,
				Target:        RefOrOid{OID: commit1},
				UpdateWorkDir: true,
			})
			repo.Close()
			if err != nil {
				t.Fatalf("switch failed: %v", err)
			}
			if result.Success || result.Conflict == nil {
				t.Fatal("expected conflict switching to commit1 with staged LICENSE")
			}
			if len(result.Conflict.StaleFiles) != 1 {
				t.Fatalf("expected 1 stale file, got %d", len(result.Conflict.StaleFiles))
			}

			// delete the file
			os.Remove(filepath.Join(workPath, "LICENSE"))
			err = Run(opts, []string{"add", "LICENSE"}, workPath, runOpts)
			if err != nil {
				t.Fatalf("add LICENSE (delete) failed: %v", err)
			}
		}

		// make a new file (only in the work dir) that conflicts with the descendent of a file from commit1
		{
			writeFile(t, workPath, "docs", "i conflict with the docs dir in the first commit")

			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			result, err := repo.Switch(SwitchInput{
				Kind:          SwitchKindSwitch,
				Target:        RefOrOid{OID: commit1},
				UpdateWorkDir: true,
			})
			repo.Close()
			if err != nil {
				t.Fatalf("switch failed: %v", err)
			}
			if result.Success || result.Conflict == nil {
				t.Fatal("expected conflict switching to commit1 with docs file")
			}

			os.Remove(filepath.Join(workPath, "docs"))
		}

		// change a file so it conflicts with the one in commit1
		{
			writeFile(t, workPath, "hello.txt", "12345")

			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			result, err := repo.Switch(SwitchInput{
				Kind:          SwitchKindSwitch,
				Target:        RefOrOid{OID: commit1},
				UpdateWorkDir: true,
			})
			repo.Close()
			if err != nil {
				t.Fatalf("switch failed: %v", err)
			}
			if result.Success || result.Conflict == nil {
				t.Fatal("expected conflict switching to commit1 with modified hello.txt")
			}
			if len(result.Conflict.StaleFiles) != 1 {
				t.Fatalf("expected 1 stale file, got %d", len(result.Conflict.StaleFiles))
			}

			// change the file back
			writeFile(t, workPath, "hello.txt", newHelloTxtContent)
		}

		// create a dir with a file that conflicts with one in commit1
		{
			licenseDir := filepath.Join(workPath, "LICENSE")
			if err := os.MkdirAll(licenseDir, 0755); err != nil {
				t.Fatalf("mkdir LICENSE failed: %v", err)
			}
			writeFile(t, licenseDir, "foo.txt", "foo")

			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			result, err := repo.Switch(SwitchInput{
				Kind:          SwitchKindSwitch,
				Target:        RefOrOid{OID: commit1},
				UpdateWorkDir: true,
			})
			repo.Close()
			if err != nil {
				t.Fatalf("switch failed: %v", err)
			}
			if result.Success || result.Conflict == nil {
				t.Fatal("expected conflict switching to commit1 with LICENSE dir")
			}
			if len(result.Conflict.StaleDirs) != 1 {
				t.Fatalf("expected 1 stale dir, got %d", len(result.Conflict.StaleDirs))
			}

			os.RemoveAll(filepath.Join(workPath, "LICENSE"))
		}
	}

	// switch to first commit
	err = Run(opts, []string{"switch", commit1}, workPath, runOpts)
	if err != nil {
		t.Fatalf("switch to commit1 failed: %v", err)
	}

	// the work dir was updated
	{
		content, err := os.ReadFile(filepath.Join(workPath, "hello.txt"))
		if err != nil {
			t.Fatalf("read hello.txt failed: %v", err)
		}
		if string(content) != helloTxtContent {
			t.Fatalf("hello.txt content after switch = %q, want %q", string(content), helloTxtContent)
		}

		// LICENSE should exist
		if _, err := os.Stat(filepath.Join(workPath, "LICENSE")); err != nil {
			t.Fatalf("LICENSE should exist after switch to commit1: %v", err)
		}

		// one/two should not exist
		if _, err := os.Stat(filepath.Join(workPath, "one", "two")); err == nil {
			t.Fatal("one/two should not exist after switch to commit1")
		}
		if _, err := os.Stat(filepath.Join(workPath, "one")); err == nil {
			t.Fatal("one should not exist after switch to commit1")
		}
	}

	// switch to master
	err = Run(opts, []string{"switch", "master"}, workPath, runOpts)
	if err != nil {
		t.Fatalf("switch to master failed: %v", err)
	}

	// the work dir was updated
	{
		content, err := os.ReadFile(filepath.Join(workPath, "hello.txt"))
		if err != nil {
			t.Fatalf("read hello.txt failed: %v", err)
		}
		if string(content) != newHelloTxtContent {
			t.Fatalf("hello.txt content after switch to master = %q, want %q", string(content), newHelloTxtContent)
		}

		// LICENSE should not exist
		if _, err := os.Stat(filepath.Join(workPath, "LICENSE")); !os.IsNotExist(err) {
			t.Fatal("LICENSE should not exist after switch to master")
		}
	}

	// replacing file with dir and dir with file
	{
		// replace file with directory
		os.Remove(filepath.Join(workPath, "hello.txt"))
		helloTxtDir := filepath.Join(workPath, "hello.txt")
		if err := os.MkdirAll(helloTxtDir, 0755); err != nil {
			t.Fatalf("mkdir hello.txt dir failed: %v", err)
		}
		writeFile(t, helloTxtDir, "nested.txt", "")
		writeFile(t, helloTxtDir, "nested2.txt", "")

		// add the new dir
		err = Run(opts, []string{"add", "hello.txt"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add hello.txt dir failed: %v", err)
		}

		// read index and verify entries
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			idx, err := repo.readIndex()
			repo.Close()
			if err != nil {
				t.Fatalf("read index failed: %v", err)
			}

			expectedCount := 8
			actualCount := countIndexEntries(idx)
			if actualCount != expectedCount {
				t.Fatalf("index entry count = %d, want %d", actualCount, expectedCount)
			}

			for _, p := range []string{
				"README", "src/zig/main.zig", "tests/main_test.zig",
				"hello.txt/nested.txt", "hello.txt/nested2.txt",
				"run.sh", "one/two/three.txt", "three.txt",
			} {
				if _, ok := idx.entries[p]; !ok {
					t.Fatalf("expected index entry %q not found", p)
				}
			}
		}

		// replace directory with file
		os.Remove(filepath.Join(workPath, "hello.txt", "nested.txt"))
		os.Remove(filepath.Join(workPath, "hello.txt", "nested2.txt"))
		os.Remove(filepath.Join(workPath, "hello.txt"))
		writeFile(t, workPath, "hello.txt", "")

		// add the new file
		err = Run(opts, []string{"add", "hello.txt"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add hello.txt file failed: %v", err)
		}

		// read index and verify entries
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			idx, err := repo.readIndex()
			repo.Close()
			if err != nil {
				t.Fatalf("read index failed: %v", err)
			}

			expectedCount := 7
			actualCount := countIndexEntries(idx)
			if actualCount != expectedCount {
				t.Fatalf("index entry count = %d, want %d", actualCount, expectedCount)
			}

			for _, p := range []string{
				"README", "src/zig/main.zig", "tests/main_test.zig",
				"hello.txt", "run.sh", "one/two/three.txt", "three.txt",
			} {
				if _, ok := idx.entries[p]; !ok {
					t.Fatalf("expected index entry %q not found", p)
				}
			}
		}

		// a stale index lock file isn't hanging around
		lockPath := filepath.Join(gitDir, "index.lock")
		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Fatal("stale index.lock file exists")
		}
	}

	// changing the index
	{
		// can't add a non-existent file
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			err = repo.Add([]string{"no-such-file"})
			repo.Close()
			if err != ErrAddIndexPathNotFound {
				t.Fatalf("expected ErrAddIndexPathNotFound, got %v", err)
			}
		}

		// can't remove non-existent file
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			err = repo.Remove([]string{"no-such-file"}, RemoveOptions{UpdateWorkDir: true})
			repo.Close()
			if err != ErrRemoveIndexPathNotFound {
				t.Fatalf("expected ErrRemoveIndexPathNotFound, got %v", err)
			}
		}

		// modify a file
		writeFile(t, filepath.Join(workPath, "one", "two"), "three.txt", "this is now modified")

		// can't remove a file with unstaged changes
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			err = repo.Remove([]string{"one/two/three.txt"}, RemoveOptions{UpdateWorkDir: true})
			repo.Close()
			if err != ErrCannotRemoveFileWithUnstagedChanges {
				t.Fatalf("expected ErrCannotRemoveFileWithUnstagedChanges, got %v", err)
			}
		}

		// stage the changes
		err = Run(opts, []string{"add", "one/two/three.txt"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add one/two/three.txt failed: %v", err)
		}

		// modify it again
		writeFile(t, filepath.Join(workPath, "one", "two"), "three.txt", "this is now modified again")

		// can't untrack a file with staged and unstaged changes
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			err = repo.Untrack([]string{"one/two/three.txt"}, false, false)
			repo.Close()
			if err != ErrCannotRemoveFileWithStagedAndUnstagedChanges {
				t.Fatalf("expected ErrCannotRemoveFileWithStagedAndUnstagedChanges, got %v", err)
			}
		}

		// add dir
		err = Run(opts, []string{"add", "one"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add one failed: %v", err)
		}

		// can't untrack a dir without -r
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			err = repo.Untrack([]string{"one"}, false, false)
			repo.Close()
			if err != ErrRecursiveOptionRequired {
				t.Fatalf("expected ErrRecursiveOptionRequired, got %v", err)
			}
		}

		// can't unadd a dir without -r
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			err = repo.Unadd([]string{"one"}, UnaddOptions{Recursive: false})
			repo.Close()
			if err != ErrRecursiveOptionRequired {
				t.Fatalf("expected ErrRecursiveOptionRequired, got %v", err)
			}
		}

		// unadd dir
		err = Run(opts, []string{"unadd", "-r", "one"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("unadd -r one failed: %v", err)
		}

		// still tracked because unadd just resets it back to the state from HEAD
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			idx, err := repo.readIndex()
			repo.Close()
			if err != nil {
				t.Fatalf("read index failed: %v", err)
			}

			entries, ok := idx.entries["one/two/three.txt"]
			if !ok || entries[0] == nil {
				t.Fatal("one/two/three.txt should still be in the index after unadd")
			}
			if entries[0].fileSize != uint32(len("one, two, three!")) {
				t.Fatalf("one/two/three.txt file size = %d, want %d", entries[0].fileSize, len("one, two, three!"))
			}
		}

		// untrack file
		err = Run(opts, []string{"untrack", "one/two/three.txt"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("untrack one/two/three.txt failed: %v", err)
		}

		// not tracked anymore
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			idx, err := repo.readIndex()
			repo.Close()
			if err != nil {
				t.Fatalf("read index failed: %v", err)
			}

			if _, ok := idx.entries["one/two/three.txt"]; ok {
				t.Fatal("one/two/three.txt should not be in the index after untrack")
			}
		}

		// stage the changes to the file
		err = Run(opts, []string{"add", "one/two/three.txt"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add one/two/three.txt failed: %v", err)
		}

		// can't remove a file with staged changes
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			err = repo.Remove([]string{"one/two/three.txt"}, RemoveOptions{UpdateWorkDir: true})
			repo.Close()
			if err != ErrCannotRemoveFileWithStagedChanges {
				t.Fatalf("expected ErrCannotRemoveFileWithStagedChanges, got %v", err)
			}
		}

		// remove file by force
		err = Run(opts, []string{"rm", "one/two/three.txt", "-f"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("rm -f one/two/three.txt failed: %v", err)
		}

		// restore file's original content
		{
			if err := os.MkdirAll(filepath.Join(workPath, "one", "two"), 0755); err != nil {
				t.Fatalf("mkdir one/two failed: %v", err)
			}
			writeFile(t, filepath.Join(workPath, "one", "two"), "three.txt", "one, two, three!")

			err = Run(opts, []string{"add", "one/two/three.txt"}, workPath, runOpts)
			if err != nil {
				t.Fatalf("add one/two/three.txt failed: %v", err)
			}
		}

		// remove a file
		{
			err = Run(opts, []string{"rm", "one/two/three.txt"}, workPath, runOpts)
			if err != nil {
				t.Fatalf("rm one/two/three.txt failed: %v", err)
			}

			// file should be gone from work dir
			if _, err := os.Stat(filepath.Join(workPath, "one", "two", "three.txt")); !os.IsNotExist(err) {
				t.Fatal("one/two/three.txt should not exist after rm")
			}
		}
	}

	// status
	{
		// make file
		writeFile(t, workPath, "goodbye.txt", "Goodbye")

		// make dirs
		for _, d := range []string{"a", "b", "c"} {
			if err := os.MkdirAll(filepath.Join(workPath, d), 0755); err != nil {
				t.Fatalf("mkdir %s failed: %v", d, err)
			}
		}

		// make file in dir
		writeFile(t, filepath.Join(workPath, "a"), "farewell.txt", "Farewell")

		// modify indexed files
		writeFile(t, workPath, "hello.txt", "hello, world again!")
		writeFile(t, workPath, "README", "My code project") // size doesn't change

		// delete an indexed file
		os.Remove(filepath.Join(workPath, "src", "zig", "main.zig"))

		// work dir changes
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			st, err := repo.Status()
			repo.Close()
			if err != nil {
				t.Fatalf("status failed: %v", err)
			}

			// check the untracked entries
			if len(st.Untracked) != 2 {
				t.Fatalf("untracked count = %d, want 2: %v", len(st.Untracked), st.Untracked)
			}
			if !st.Untracked["a"] {
				t.Fatal("expected 'a' in untracked")
			}
			if !st.Untracked["goodbye.txt"] {
				t.Fatal("expected 'goodbye.txt' in untracked")
			}

			// check the work_dir_modified entries
			if len(st.WorkDirModified) != 2 {
				t.Fatalf("work_dir_modified count = %d, want 2: %v", len(st.WorkDirModified), st.WorkDirModified)
			}
			if !st.WorkDirModified["hello.txt"] {
				t.Fatal("expected 'hello.txt' in work_dir_modified")
			}
			if !st.WorkDirModified["README"] {
				t.Fatal("expected 'README' in work_dir_modified")
			}

			// check the work_dir_deleted entries
			if len(st.WorkDirDeleted) != 1 {
				t.Fatalf("work_dir_deleted count = %d, want 1: %v", len(st.WorkDirDeleted), st.WorkDirDeleted)
			}
			if !st.WorkDirDeleted["src/zig/main.zig"] {
				t.Fatal("expected 'src/zig/main.zig' in work_dir_deleted")
			}
		}

		// index changes
		{
			// add file to index
			writeFile(t, filepath.Join(workPath, "c"), "d.txt", "")
			err = Run(opts, []string{"add", "c/d.txt"}, workPath, runOpts)
			if err != nil {
				t.Fatalf("add c/d.txt failed: %v", err)
			}

			// remove file from index (deleted from work dir)
			err = Run(opts, []string{"add", "src/zig/main.zig"}, workPath, runOpts)
			if err != nil {
				t.Fatalf("add src/zig/main.zig (deleted) failed: %v", err)
			}

			// get status
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			st, err := repo.Status()
			repo.Close()
			if err != nil {
				t.Fatalf("status failed: %v", err)
			}

			// check the index_added entries
			if len(st.IndexAdded) != 1 {
				t.Fatalf("index_added count = %d, want 1: %v", len(st.IndexAdded), st.IndexAdded)
			}
			if !st.IndexAdded["c/d.txt"] {
				t.Fatal("expected 'c/d.txt' in index_added")
			}

			// check the index_modified entries
			if len(st.IndexModified) != 1 {
				t.Fatalf("index_modified count = %d, want 1: %v", len(st.IndexModified), st.IndexModified)
			}
			if !st.IndexModified["hello.txt"] {
				t.Fatal("expected 'hello.txt' in index_modified")
			}

			// check the index_deleted entries
			if len(st.IndexDeleted) != 2 {
				t.Fatalf("index_deleted count = %d, want 2: %v", len(st.IndexDeleted), st.IndexDeleted)
			}
			if !st.IndexDeleted["src/zig/main.zig"] {
				t.Fatal("expected 'src/zig/main.zig' in index_deleted")
			}
			if !st.IndexDeleted["one/two/three.txt"] {
				t.Fatal("expected 'one/two/three.txt' in index_deleted")
			}
		}
	}

	// restore
	{
		// there are two modified and two deleted files remaining
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			st, err := repo.Status()
			repo.Close()
			if err != nil {
				t.Fatalf("status failed: %v", err)
			}
			if len(st.WorkDirModified) != 2 {
				t.Fatalf("work_dir_modified count = %d, want 2: %v", len(st.WorkDirModified), st.WorkDirModified)
			}
			if len(st.IndexDeleted) != 2 {
				t.Fatalf("index_deleted count = %d, want 2: %v", len(st.IndexDeleted), st.IndexDeleted)
			}
		}

		err = Run(opts, []string{"restore", "README"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("restore README failed: %v", err)
		}

		err = Run(opts, []string{"restore", "hello.txt"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("restore hello.txt failed: %v", err)
		}

		// directories can be restored
		err = Run(opts, []string{"restore", "src"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("restore src failed: %v", err)
		}

		// nested paths can be restored
		err = Run(opts, []string{"restore", "one/two/three.txt"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("restore one/two/three.txt failed: %v", err)
		}

		// remove changes to index
		err = Run(opts, []string{"add", "hello.txt", "src", "one"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add restored files failed: %v", err)
		}

		// there are no modified or deleted files remaining
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			st, err := repo.Status()
			repo.Close()
			if err != nil {
				t.Fatalf("status failed: %v", err)
			}
			if len(st.WorkDirModified) != 0 {
				t.Fatalf("work_dir_modified count = %d, want 0: %v", len(st.WorkDirModified), st.WorkDirModified)
			}
			if len(st.IndexDeleted) != 0 {
				t.Fatalf("index_deleted count = %d, want 0: %v", len(st.IndexDeleted), st.IndexDeleted)
			}
		}
	}
}

func countIndexEntries(idx *Index) int {
	count := 0
	for _, entries := range idx.entries {
		for _, e := range entries {
			if e != nil {
				count++
				break
			}
		}
	}
	return count
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write %s failed: %v", name, err)
	}
}
