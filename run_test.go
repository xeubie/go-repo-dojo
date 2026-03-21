package repomofo

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
	_, err = OpenRepo(workPath, opts)
	if err != nil {
		t.Fatalf("open repo failed: %v", err)
	}

	// make sure we can get status before first commit
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		_, err = repo.Status()
		if err != nil {
			t.Fatalf("status before first commit failed: %v", err)
		}
	}

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
	var commit2 string
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}

		commit2, err = repo.ReadHeadRecurMaybe()
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

	// parse objects
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}

		// read commit
		commitObj, err := repo.NewObject(commit2, true)
		if err != nil {
			t.Fatalf("read commit2 object failed: %v", err)
		}
		if commitObj.Commit.Message != "second commit" {
			t.Fatalf("commit2 message = %q, want %q", commitObj.Commit.Message, "second commit")
		}

		// read tree
		treeObj, err := repo.NewObject(commitObj.Commit.Tree, true)
		commitObj.Close()
		if err != nil {
			t.Fatalf("read tree object failed: %v", err)
		}
		defer treeObj.Close()

		if len(treeObj.Tree.Entries) != 7 {
			t.Fatalf("tree entry count = %d, want 7", len(treeObj.Tree.Entries))
		}
	}

	// remove dir from index
	{
		// make a nested dir with a few files
		if err := os.MkdirAll(filepath.Join(workPath, "foo", "bar", "baz"), 0755); err != nil {
			t.Fatalf("mkdir foo/bar/baz failed: %v", err)
		}
		writeFile(t, filepath.Join(workPath, "foo", "bar"), "hi.txt", "hi hi")
		writeFile(t, filepath.Join(workPath, "foo", "bar", "baz"), "bye.txt", "bye bye")

		// can't remove unindexed file
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			err = repo.Remove([]string{"foo/bar/hi.txt"}, RemoveOptions{UpdateWorkDir: true})
			if err != ErrRemoveIndexPathNotFound {
				t.Fatalf("expected ErrRemoveIndexPathNotFound, got %v", err)
			}
		}

		// add dir
		err = Run(opts, []string{"add", "foo"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add foo failed: %v", err)
		}

		// make a commit
		err = Run(opts, []string{"commit", "-m", "third commit"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("third commit failed: %v", err)
		}

		// untrack hi.txt
		err = Run(opts, []string{"untrack", "foo/bar/hi.txt"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("untrack foo/bar/hi.txt failed: %v", err)
		}

		// can't remove subdir without -r
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			err = repo.Remove([]string{"foo"}, RemoveOptions{UpdateWorkDir: true})
			if err != ErrRecursiveOptionRequired {
				t.Fatalf("expected ErrRecursiveOptionRequired, got %v", err)
			}
		}

		// remove subdir with -r
		err = Run(opts, []string{"rm", "-r", "foo"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("rm -r foo failed: %v", err)
		}

		// make sure baz dir was deleted
		if _, err := os.Stat(filepath.Join(workPath, "foo", "bar", "baz")); !os.IsNotExist(err) {
			t.Fatal("foo/bar/baz should not exist after rm -r foo")
		}

		// but hi.txt was not deleted, because it wasn't in the index
		if _, err := os.Stat(filepath.Join(workPath, "foo", "bar", "hi.txt")); err != nil {
			t.Fatalf("foo/bar/hi.txt should still exist: %v", err)
		}

		// add hi.txt back to the index
		err = Run(opts, []string{"add", "foo/bar/hi.txt"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add foo/bar/hi.txt failed: %v", err)
		}
	}
	// get HEAD contents (commit3)
	var commit3 string
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		commit3, err = repo.ReadHeadRecurMaybe()
		if err != nil {
			t.Fatalf("read HEAD failed: %v", err)
		}
		if commit3 == "" {
			t.Fatal("commit3 is empty")
		}
	}

	// create a branch
	err = Run(opts, []string{"branch", "add", "stuff"}, workPath, runOpts)
	if err != nil {
		t.Fatalf("branch add stuff failed: %v", err)
	}

	// switch to the branch
	err = Run(opts, []string{"switch", "stuff"}, workPath, runOpts)
	if err != nil {
		t.Fatalf("switch stuff failed: %v", err)
	}

	// check the refs
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}

		headOID, err := repo.ReadHeadRecurMaybe()
		if err != nil {
			t.Fatalf("read HEAD failed: %v", err)
		}
		if headOID != commit3 {
			t.Fatalf("HEAD = %s, want %s", headOID, commit3)
		}

		stuffOID, err := repo.ReadRef(Ref{Kind: RefHead, Name: "stuff"})
		if err != nil {
			t.Fatalf("read stuff ref failed: %v", err)
		}
		if stuffOID != commit3 {
			t.Fatalf("stuff ref = %s, want %s", stuffOID, commit3)
		}
	}

	// list all branches
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		iter, err := repo.listBranches()
		if err != nil {
			t.Fatalf("list branches failed: %v", err)
		}
		defer iter.Close()
		count := 0
		for {
			ref, err := iter.Next()
			if err != nil {
				t.Fatalf("branch iter next failed: %v", err)
			}
			if ref == nil {
				break
			}
			count++
		}
		if count != 2 {
			t.Fatalf("branch count = %d, want 2", count)
		}
	}

	// get the current branch
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		head, err := repo.Head()
		if err != nil {
			t.Fatalf("head failed: %v", err)
		}
		if !head.IsRef {
			t.Fatal("expected HEAD to be a ref")
		}
		if head.Ref.Name != "stuff" {
			t.Fatalf("current branch = %q, want %q", head.Ref.Name, "stuff")
		}
	}

	// can't delete current branch
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		err = repo.removeBranch(RemoveBranchInput{Name: "stuff"})
		if err != ErrCannotDeleteCurrentBranch {
			t.Fatalf("expected ErrCannotDeleteCurrentBranch, got %v", err)
		}
	}

	// make a few commits on the stuff branch
	{
		writeFile(t, workPath, "hello.txt", "hello, world on the stuff branch, commit 3!")

		// add the files
		err = Run(opts, []string{"add", "hello.txt"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add hello.txt failed: %v", err)
		}

		// make a commit
		err = Run(opts, []string{"commit", "-m", "third commit"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("third commit on stuff failed: %v", err)
		}

		writeFile(t, workPath, "stuff.txt", "this was made on the stuff branch, commit 4!")

		// add the files
		err = Run(opts, []string{"add", "stuff.txt"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add stuff.txt failed: %v", err)
		}

		// make a commit
		err = Run(opts, []string{"commit", "-m", "fourth commit"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("fourth commit on stuff failed: %v", err)
		}
	}

	// get HEAD contents (commit4_stuff)
	var commit4Stuff string
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		commit4Stuff, err = repo.ReadHeadRecurMaybe()
		if err != nil {
			t.Fatalf("read HEAD failed: %v", err)
		}
		if commit4Stuff == "" {
			t.Fatal("commit4Stuff is empty")
		}
	}
	_ = commit4Stuff

	// create a branch with slashes
	err = Run(opts, []string{"branch", "add", "a/b/c"}, workPath, runOpts)
	if err != nil {
		t.Fatalf("branch add a/b/c failed: %v", err)
	}

	// make sure the ref is created with subdirs
	{
		refPath := filepath.Join(gitDir, "refs", "heads", "a", "b", "c")
		if _, err := os.Stat(refPath); err != nil {
			t.Fatalf("refs/heads/a/b/c not found: %v", err)
		}
	}

	// list all branches
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		iter, err := repo.listBranches()
		if err != nil {
			t.Fatalf("list branches failed: %v", err)
		}
		defer iter.Close()
		branchSet := map[string]bool{}
		for {
			ref, err := iter.Next()
			if err != nil {
				t.Fatalf("branch iter next failed: %v", err)
			}
			if ref == nil {
				break
			}
			branchSet[ref.Name] = true
		}
		if len(branchSet) != 3 {
			t.Fatalf("branch count = %d, want 3", len(branchSet))
		}
		for _, name := range []string{"a/b/c", "stuff", "master"} {
			if !branchSet[name] {
				t.Fatalf("expected branch %q not found", name)
			}
		}
	}

	// remove the branch
	err = Run(opts, []string{"branch", "rm", "a/b/c"}, workPath, runOpts)
	if err != nil {
		t.Fatalf("branch rm a/b/c failed: %v", err)
	}

	// make sure the subdirs are deleted
	{
		for _, p := range []string{
			filepath.Join(gitDir, "refs", "heads", "a", "b", "c"),
			filepath.Join(gitDir, "refs", "heads", "a", "b"),
			filepath.Join(gitDir, "refs", "heads", "a"),
		} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("%s should not exist after branch rm a/b/c", p)
			}
		}
	}

	// switch to master
	err = Run(opts, []string{"switch", "master"}, workPath, runOpts)
	if err != nil {
		t.Fatalf("switch to master failed: %v", err)
	}

	// modify files and commit
	{
		writeFile(t, workPath, "hello.txt", "hello, world once again!")
		writeFile(t, workPath, "goodbye.txt", "goodbye, world once again!")

		// add the files
		err = Run(opts, []string{"add", "hello.txt", "goodbye.txt"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("add hello.txt goodbye.txt failed: %v", err)
		}

		// make a commit
		err = Run(opts, []string{"commit", "-m", "fourth commit"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("fourth commit on master failed: %v", err)
		}
	}

	// get HEAD contents (commit4)
	var commit4 string
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		commit4, err = repo.ReadHeadRecurMaybe()
		if err != nil {
			t.Fatalf("read HEAD failed: %v", err)
		}
		if commit4 == "" {
			t.Fatal("commit4 is empty")
		}
	}

	// make sure the most recent branch name points to the most recent commit
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		masterOID, err := repo.ReadRef(Ref{Kind: RefHead, Name: "master"})
		if err != nil {
			t.Fatalf("read master ref failed: %v", err)
		}
		if masterOID != commit4 {
			t.Fatalf("master ref = %s, want %s", masterOID, commit4)
		}
	}

	// log
	{
		repo, err := OpenRepo(workPath, opts)
		if err != nil {
			t.Fatalf("open repo failed: %v", err)
		}
		iter, err := repo.Log(nil)
		if err != nil {
			t.Fatalf("log failed: %v", err)
		}

		type logEntry struct {
			oid     string
			message string
		}
		var entries []logEntry
		for {
			rawObj, err := iter.Next()
			if err != nil {
				t.Fatalf("log next failed: %v", err)
			}
			if rawObj == nil {
				break
			}
			rawObj.Close()

			obj, err := repo.NewObject(rawObj.OID, true)
			if err != nil {
				t.Fatalf("read commit failed: %v", err)
			}
			entries = append(entries, logEntry{oid: obj.OID, message: obj.Commit.Message})
			obj.Close()
		}

		if len(entries) != 4 {
			t.Fatalf("log entry count = %d, want 4", len(entries))
		}
		if entries[0].oid != commit4 || entries[0].message != "fourth commit" {
			t.Fatalf("log[0] = {%s, %q}, want {%s, %q}", entries[0].oid, entries[0].message, commit4, "fourth commit")
		}
		if entries[1].oid != commit3 || entries[1].message != "third commit" {
			t.Fatalf("log[1] = {%s, %q}, want {%s, %q}", entries[1].oid, entries[1].message, commit3, "third commit")
		}
		if entries[2].oid != commit2 || entries[2].message != "second commit" {
			t.Fatalf("log[2] = {%s, %q}, want {%s, %q}", entries[2].oid, entries[2].message, commit2, "second commit")
		}
		if entries[3].oid != commit1 || entries[3].message != "first commit" {
			t.Fatalf("log[3] = {%s, %q}, want {%s, %q}", entries[3].oid, entries[3].message, commit1, "first commit")
		}
	}

	// config
	{
		err = Run(opts, []string{"config", "add", "core.editor", "vim"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("config add core.editor failed: %v", err)
		}
		err = Run(opts, []string{"config", "add", "branch.master.remote", "origin"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("config add branch.master.remote failed: %v", err)
		}

		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			config, err := repo.ListConfig()
			if err != nil {
				t.Fatalf("list config failed: %v", err)
			}

			coreSection := findConfigSection(config, "core")
			if coreSection == nil {
				t.Fatal("core section not found")
			}
			if len(coreSection.variables) != 1 {
				t.Fatalf("core variable count = %d, want 1", len(coreSection.variables))
			}

			branchSection := findConfigSection(config, "branch.master")
			if branchSection == nil {
				t.Fatal("branch.master section not found")
			}
			if len(branchSection.variables) != 1 {
				t.Fatalf("branch.master variable count = %d, want 1", len(branchSection.variables))
			}
			if val := findConfigVar(branchSection, "remote"); val != "origin" {
				t.Fatalf("branch.master.remote = %q, want %q", val, "origin")
			}
		}

		err = Run(opts, []string{"config", "rm", "branch.master.remote"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("config rm branch.master.remote failed: %v", err)
		}

		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			config, err := repo.ListConfig()
			if err != nil {
				t.Fatalf("list config failed: %v", err)
			}
			if findConfigSection(config, "branch.master") != nil {
				t.Fatal("branch.master section should not exist after rm")
			}
		}

		// don't allow invalid names
		err = Run(opts, []string{"config", "add", "core.editor#hi", "vim"}, workPath, runOpts)
		if err == nil {
			t.Fatal("expected error for invalid config name")
		}

		// do allow values with spaces
		err = Run(opts, []string{"config", "add", "user.name", "radar", "roark"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("config add user.name failed: %v", err)
		}
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			config, err := repo.ListConfig()
			if err != nil {
				t.Fatalf("list config failed: %v", err)
			}
			userSection := findConfigSection(config, "user")
			if userSection == nil {
				t.Fatal("user section not found")
			}
			if val := findConfigVar(userSection, "name"); val != "radar roark" {
				t.Fatalf("user.name = %q, want %q", val, "radar roark")
			}
		}

		// do allow additional characters in subsection names
		err = Run(opts, []string{"config", "add", "branch.\"hello.world\".remote", "radar roark"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("config add branch.hello.world.remote failed: %v", err)
		}
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			config, err := repo.ListConfig()
			if err != nil {
				t.Fatalf("list config failed: %v", err)
			}
			section := findConfigSection(config, "branch.\"hello.world\"")
			if section == nil {
				t.Fatal("branch.\"hello.world\" section not found")
			}
			if len(section.variables) != 1 {
				t.Fatalf("branch.\"hello.world\" variable count = %d, want 1", len(section.variables))
			}
		}

		// section and var names are forcibly lower-cased, but not the subsection name
		err = Run(opts, []string{"config", "add", "BRANCH.MASTER.REMOTE", "origin"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("config add BRANCH.MASTER.REMOTE failed: %v", err)
		}
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			config, err := repo.ListConfig()
			if err != nil {
				t.Fatalf("list config failed: %v", err)
			}
			section := findConfigSection(config, "branch.MASTER")
			if section == nil {
				t.Fatal("branch.MASTER section not found")
			}
			if len(section.variables) != 1 {
				t.Fatalf("branch.MASTER variable count = %d, want 1", len(section.variables))
			}
			if val := findConfigVar(section, "remote"); val != "origin" {
				t.Fatalf("branch.MASTER.remote = %q, want %q", val, "origin")
			}
		}

		err = Run(opts, []string{"config", "list"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("config list failed: %v", err)
		}
	}

	// remote
	{
		err = Run(opts, []string{"remote", "add", "origin", "http://localhost:3000"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("remote add origin failed: %v", err)
		}
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			remote, err := repo.ListRemotes()
			if err != nil {
				t.Fatalf("list remotes failed: %v", err)
			}
			if findConfigSection(remote, "origin") == nil {
				t.Fatal("origin remote not found")
			}
		}

		err = Run(opts, []string{"remote", "rm", "origin"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("remote rm origin failed: %v", err)
		}
		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}
			remote, err := repo.ListRemotes()
			if err != nil {
				t.Fatalf("list remotes failed: %v", err)
			}
			if findConfigSection(remote, "origin") != nil {
				t.Fatal("origin remote should not exist after rm")
			}
		}

		err = Run(opts, []string{"remote", "list"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("remote list failed: %v", err)
		}
	}

	// tag
	{
		err = Run(opts, []string{"tag", "add", "ann", "-m", "this is an annotated tag"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("tag add ann failed: %v", err)
		}

		{
			repo, err := OpenRepo(workPath, opts)
			if err != nil {
				t.Fatalf("open repo failed: %v", err)
			}

			tagOID, err := repo.ReadRef(Ref{Kind: RefTag, Name: "ann"})
			if err != nil {
				t.Fatalf("read tag ref failed: %v", err)
			}

			obj, err := repo.NewObject(tagOID, true)
			if err != nil {
				t.Fatalf("read tag object failed: %v", err)
			}
			defer obj.Close()

			if obj.Tag == nil {
				t.Fatal("expected tag content")
			}
			if obj.Tag.Message != "this is an annotated tag" {
				t.Fatalf("tag message = %q, want %q", obj.Tag.Message, "this is an annotated tag")
			}
		}

		err = Run(opts, []string{"tag", "list"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("tag list failed: %v", err)
		}

		err = Run(opts, []string{"tag", "rm", "ann"}, workPath, runOpts)
		if err != nil {
			t.Fatalf("tag rm ann failed: %v", err)
		}
	}
}

func findConfigSection(config *Config, name string) *configSection {
	for i := range config.sections {
		if config.sections[i].name == name {
			return &config.sections[i]
		}
	}
	return nil
}

func findConfigVar(section *configSection, name string) string {
	for _, v := range section.variables {
		if v.name == name {
			return v.value
		}
	}
	return ""
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
