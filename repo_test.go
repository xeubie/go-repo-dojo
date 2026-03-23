package repomofo

import (
	"os"
	"path/filepath"
	"strings"
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
	t.Run("fileObjectStore", func(t *testing.T) {
		Simple(t, nil)
	})
	t.Run("memoryObjectStore", func(t *testing.T) {
		Simple(t, newMemoryObjectStore(SHA1HashKind))
	})
}

func Simple(t *testing.T, store ObjectStore) {
	t.Helper()
	tempDir := t.TempDir()
	workPath := filepath.Join(tempDir, "repo")

	opts := RepoOpts{
		Hash:   SHA1HashKind,
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
			commitA.Hex(): true,
			commitB.Hex(): true,
			commitC.Hex(): true,
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
			delete(oidSet, rawObj.OID.Hex())
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
			Target:        OIDValue{OID: commitB},
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
			Target:        OIDValue{OID: commitA},
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
			Target:        OIDValue{OID: commitC},
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
			Target:        RefValue{Ref: Ref{Kind: RefTag, Name: "1.0.0"}},
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

func TestMerge(t *testing.T) {
	tempDir := t.TempDir()
	workPath := filepath.Join(tempDir, "repo")

	opts := RepoOpts{Hash: SHA1HashKind, IsTest: true}

	_, err := InitRepo(workPath, opts)
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}
	repo, err := OpenRepo(workPath, opts)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}

	// A --- B --- C --------- J --- K [master]
	//        \               /
	//         \             /
	//          D --- E --- F [foo]
	//           \
	//            \
	//             G --- H [bar]

	addFile(t, repo, "master.md", "a")
	if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "master.md", "b")
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}

	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Switch(SwitchInput{Kind: SwitchKindSwitch, Target: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}, UpdateWorkDir: true}); err != nil {
		t.Fatal(err)
	}

	addFile(t, repo, "foo.md", "d")
	commitD, err := repo.Commit(CommitMetadata{Message: "d"})
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.AddBranch(AddBranchInput{Name: "bar"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Switch(SwitchInput{Kind: SwitchKindSwitch, Target: RefValue{Ref: Ref{Kind: RefHead, Name: "bar"}}, UpdateWorkDir: true}); err != nil {
		t.Fatal(err)
	}

	addFile(t, repo, "bar.md", "g")
	if _, err := repo.Commit(CommitMetadata{Message: "g"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "bar.md", "h")
	commitH, err := repo.Commit(CommitMetadata{Message: "h"})
	if err != nil {
		t.Fatal(err)
	}
	_ = commitH

	if _, err := repo.Switch(SwitchInput{Kind: SwitchKindSwitch, Target: RefValue{Ref: Ref{Kind: RefHead, Name: "master"}}, UpdateWorkDir: true}); err != nil {
		t.Fatal(err)
	}

	addFile(t, repo, "master.md", "c")
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.Switch(SwitchInput{Kind: SwitchKindSwitch, Target: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}, UpdateWorkDir: true}); err != nil {
		t.Fatal(err)
	}

	addFile(t, repo, "foo.md", "e")
	if _, err := repo.Commit(CommitMetadata{Message: "e"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "foo.md", "f")
	if _, err := repo.Commit(CommitMetadata{Message: "f"}); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.Switch(SwitchInput{Kind: SwitchKindSwitch, Target: RefValue{Ref: Ref{Kind: RefHead, Name: "master"}}, UpdateWorkDir: true}); err != nil {
		t.Fatal(err)
	}

	// merge foo into master
	mergeResult, err := repo.Merge(MergeInput{
		Kind:   MergeKindFull,
		Action: MergeActionNew{Source: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mergeSuccess, ok := mergeResult.Result.(MergeResultSuccess)
	if !ok {
		t.Fatalf("expected success, got %v", mergeResult)
	}
	commitJ := mergeSuccess.OID

	addFile(t, repo, "master.md", "k")
	commitK, err := repo.Commit(CommitMetadata{Message: "k"})
	if err != nil {
		t.Fatal(err)
	}

	// there are multiple common ancestors, b and d,
	// but d is the best one because it is a descendent of b
	ancestorKH, err := commonAncestor(repo, commitK, commitH)
	if err != nil {
		t.Fatal(err)
	}
	if ancestorKH != commitD {
		t.Fatalf("expected ancestor %s, got %s", commitD, ancestorKH)
	}

	// if one commit is an ancestor of the other, it is the best common ancestor
	ancestorKJ, err := commonAncestor(repo, commitK, commitJ)
	if err != nil {
		t.Fatal(err)
	}
	if ancestorKJ != commitJ {
		t.Fatalf("expected ancestor %s, got %s", commitJ, ancestorKJ)
	}

	// if we try merging foo again, it does nothing
	{
		mergeResult, err := repo.Merge(MergeInput{
			Kind:   MergeKindFull,
			Action: MergeActionNew{Source: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := mergeResult.Result.(MergeResultNothing); !ok {
			t.Fatalf("expected nothing, got %v", mergeResult)
		}
	}

	// if we try merging master into foo, it fast forwards
	{
		if _, err := repo.Switch(SwitchInput{Kind: SwitchKindSwitch, Target: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}, UpdateWorkDir: true}); err != nil {
			t.Fatal(err)
		}
		mergeResult, err := repo.Merge(MergeInput{
			Kind:   MergeKindFull,
			Action: MergeActionNew{Source: RefValue{Ref: Ref{Kind: RefHead, Name: "master"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := mergeResult.Result.(MergeResultFastForward); !ok {
			t.Fatalf("expected fast_forward, got %v", mergeResult)
		}

		headOID, err := repo.ReadHeadRecurMaybe()
		if err != nil {
			t.Fatal(err)
		}
		if headOID != commitK {
			t.Fatalf("expected HEAD %s, got %s", commitK, headOID)
		}

		// make sure file from commit k exists
		content, err := os.ReadFile(filepath.Join(workPath, "master.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "k" {
			t.Fatalf("expected 'k', got %q", string(content))
		}
	}
}

func TestMergeSideBranch(t *testing.T) {
	tempDir := t.TempDir()
	workPath := filepath.Join(tempDir, "repo")

	opts := RepoOpts{Hash: SHA1HashKind, IsTest: true}

	_, err := InitRepo(workPath, opts)
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}
	repo, err := OpenRepo(workPath, opts)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}

	//           C <------ D [side]
	//          /           \
	//         /             \
	// A <--- B <---- E <---- F <---- G [master]
	//                 \
	//                  \
	//                   \
	//                    H <---- I <---- J [topic]

	addFile(t, repo, "master.md", "a")
	if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
		t.Fatal(err)
	}

	addFile(t, repo, "master.md", "b")
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}

	if err := repo.AddBranch(AddBranchInput{Name: "side"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Switch(SwitchInput{Kind: SwitchKindSwitch, Target: RefValue{Ref: Ref{Kind: RefHead, Name: "side"}}, UpdateWorkDir: true}); err != nil {
		t.Fatal(err)
	}

	addFile(t, repo, "side.md", "c")
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}

	addFile(t, repo, "side.md", "d")
	if _, err := repo.Commit(CommitMetadata{Message: "d"}); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.Switch(SwitchInput{Kind: SwitchKindSwitch, Target: RefValue{Ref: Ref{Kind: RefHead, Name: "master"}}, UpdateWorkDir: true}); err != nil {
		t.Fatal(err)
	}

	addFile(t, repo, "master.md", "e")
	commitE, err := repo.Commit(CommitMetadata{Message: "e"})
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.AddBranch(AddBranchInput{Name: "topic"}); err != nil {
		t.Fatal(err)
	}

	// commit f (merge side into master)
	mergeResult, err := repo.Merge(MergeInput{
		Kind:   MergeKindFull,
		Action: MergeActionNew{Source: RefValue{Ref: Ref{Kind: RefHead, Name: "side"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mergeResult.Result.(MergeResultSuccess); !ok {
		t.Fatalf("expected success, got %v", mergeResult)
	}

	addFile(t, repo, "master.md", "g")
	commitG, err := repo.Commit(CommitMetadata{Message: "g"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repo.Switch(SwitchInput{Kind: SwitchKindSwitch, Target: RefValue{Ref: Ref{Kind: RefHead, Name: "topic"}}, UpdateWorkDir: true}); err != nil {
		t.Fatal(err)
	}

	addFile(t, repo, "topic.md", "h")
	if _, err := repo.Commit(CommitMetadata{Message: "h"}); err != nil {
		t.Fatal(err)
	}

	addFile(t, repo, "topic.md", "i")
	if _, err := repo.Commit(CommitMetadata{Message: "i"}); err != nil {
		t.Fatal(err)
	}

	addFile(t, repo, "topic.md", "j")
	commitJ, err := repo.Commit(CommitMetadata{Message: "j"})
	if err != nil {
		t.Fatal(err)
	}

	ancestorGJ, err := commonAncestor(repo, commitG, commitJ)
	if err != nil {
		t.Fatal(err)
	}
	if ancestorGJ != commitE {
		t.Fatalf("expected ancestor %s, got %s", commitE, ancestorGJ)
	}
}

func initTestRepo(t *testing.T) *Repo {
	t.Helper()
	tempDir := t.TempDir()
	workPath := filepath.Join(tempDir, "repo")
	opts := RepoOpts{Hash: SHA1HashKind, IsTest: true}
	if _, err := InitRepo(workPath, opts); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(workPath, opts)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func switchBranch(t *testing.T, repo *Repo, name string) {
	t.Helper()
	if _, err := repo.Switch(SwitchInput{Kind: SwitchKindSwitch, Target: RefValue{Ref: Ref{Kind: RefHead, Name: name}}, UpdateWorkDir: true}); err != nil {
		t.Fatal(err)
	}
}

func mergeFoo(t *testing.T, repo *Repo) MergeData {
	t.Helper()
	data, err := repo.Merge(MergeInput{
		Kind:   MergeKindFull,
		Action: MergeActionNew{Source: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readWorkFile(t *testing.T, repo *Repo, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo.workPath, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMergeConflictSameFile(t *testing.T) {
	repo := initTestRepo(t)

	// A --- B --- D [master]
	//  \         /
	//   \       /
	//    `---- C [foo]

	addFile(t, repo, "f.txt", "a\nb\nc")
	if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "f.txt", "a\nx\nc")
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")
	addFile(t, repo, "f.txt", "a\ny\nc")
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "master")

	result := mergeFoo(t, repo)
	if _, ok := result.Result.(MergeResultConflict); !ok {
		t.Fatalf("expected conflict, got %v", result)
	}

	// verify f.txt has conflict markers
	content := readWorkFile(t, repo, "f.txt")
	if !strings.Contains(content, "<<<<<<< target (master)") {
		t.Fatalf("expected conflict markers in f.txt, got: %s", content)
	}
	if !strings.Contains(content, ">>>>>>> source (foo)") {
		t.Fatalf("expected source marker in f.txt, got: %s", content)
	}

	// can't merge again with an unresolved merge
	_, err := repo.Merge(MergeInput{
		Kind:   MergeKindFull,
		Action: MergeActionNew{Source: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}},
	})
	if err == nil {
		t.Fatal("expected error for merge during unresolved conflict")
	}

	// can't continue merge with unresolved conflicts
	_, err = repo.Merge(MergeInput{Kind: MergeKindFull, Action: MergeActionCont{}})
	if err == nil {
		t.Fatal("expected error for continue with unresolved conflicts")
	}

	// resolve conflict and continue
	addFile(t, repo, "f.txt", "a\nx\nc")
	result2, err := repo.Merge(MergeInput{Kind: MergeKindFull, Action: MergeActionCont{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result2.Result.(MergeResultSuccess); !ok {
		t.Fatalf("expected success, got %v", result2)
	}

	// merging foo again does nothing
	result3 := mergeFoo(t, repo)
	if _, ok := result3.Result.(MergeResultNothing); !ok {
		t.Fatalf("expected nothing, got %v", result3)
	}
}

func TestMergeConflictSameFileEmptyBase(t *testing.T) {
	repo := initTestRepo(t)

	// A --- B --- D [master]
	//  \         /
	//   \       /
	//    `---- C [foo]
	// commit A (base) is empty

	if _, err := repo.Commit(CommitMetadata{Message: "a", AllowEmpty: true}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "f.txt", "a\nx\nc\n")
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")
	addFile(t, repo, "f.txt", "a\ny\nc\n")
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "master")

	result := mergeFoo(t, repo)
	if _, ok := result.Result.(MergeResultConflict); !ok {
		t.Fatalf("expected conflict, got %v", result)
	}

	// verify f.txt has conflict markers
	content := readWorkFile(t, repo, "f.txt")
	if !strings.Contains(content, "<<<<<<< target (master)") {
		t.Fatalf("expected conflict markers in f.txt, got: %s", content)
	}

	// can't merge again with an unresolved merge
	_, err := repo.Merge(MergeInput{
		Kind:   MergeKindFull,
		Action: MergeActionNew{Source: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}},
	})
	if err == nil {
		t.Fatal("expected error for merge during unresolved conflict")
	}

	// can't continue merge with unresolved conflicts
	_, err = repo.Merge(MergeInput{Kind: MergeKindFull, Action: MergeActionCont{}})
	if err == nil {
		t.Fatal("expected error for continue with unresolved conflicts")
	}

	// resolve conflict and continue
	addFile(t, repo, "f.txt", "a\ny\nc\n")
	result2, err := repo.Merge(MergeInput{Kind: MergeKindFull, Action: MergeActionCont{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result2.Result.(MergeResultSuccess); !ok {
		t.Fatalf("expected success, got %v", result2)
	}

	// merging foo again does nothing
	result3 := mergeFoo(t, repo)
	if _, ok := result3.Result.(MergeResultNothing); !ok {
		t.Fatalf("expected nothing, got %v", result3)
	}
}

func TestMergeConflictSameFileAutoresolved(t *testing.T) {
	repo := initTestRepo(t)

	// A --- B --- D [master]
	//  \         /
	//   \       /
	//    `---- C [foo]

	addFile(t, repo, "f.txt", "a\nb\nc")
	if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "f.txt", "x\nb\nc")
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")
	addFile(t, repo, "f.txt", "a\nb\ny")
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "master")

	result := mergeFoo(t, repo)
	if _, ok := result.Result.(MergeResultSuccess); !ok {
		t.Fatalf("expected success (autoresolved), got %v", result)
	}

	// verify f.txt has been autoresolved
	content := readWorkFile(t, repo, "f.txt")
	if content != "x\nb\ny" {
		t.Fatalf("expected autoresolved content, got %q", content)
	}

	// merging foo again does nothing
	result2 := mergeFoo(t, repo)
	if _, ok := result2.Result.(MergeResultNothing); !ok {
		t.Fatalf("expected nothing, got %v", result2)
	}
}

func TestMergeConflictSameFileAutoresolvedNeighboringLines(t *testing.T) {
	repo := initTestRepo(t)

	// A --- B --- D [master]
	//  \         /
	//   \       /
	//    `---- C [foo]

	addFile(t, repo, "f.txt", "a\nb\nc\nd")
	if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "f.txt", "a\nb\ne\nd")
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")
	addFile(t, repo, "f.txt", "a\nf\nc\nd")
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "master")

	result := mergeFoo(t, repo)
	// for git backend (diff3), neighboring line changes produce a conflict
	if _, ok := result.Result.(MergeResultConflict); !ok {
		t.Fatalf("expected conflict, got %v", result)
	}
}

func TestMergeConflictModifyDelete(t *testing.T) {
	repo := initTestRepo(t)

	// target modifies, source deletes

	addFile(t, repo, "f.txt", "1")
	if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "f.txt", "2")
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")
	if err := repo.Remove([]string{"f.txt"}, RemoveOptions{UpdateWorkDir: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "master")

	result := mergeFoo(t, repo)
	if _, ok := result.Result.(MergeResultConflict); !ok {
		t.Fatalf("expected conflict, got %v", result)
	}

	// can't merge again with an unresolved merge
	_, err := repo.Merge(MergeInput{
		Kind:   MergeKindFull,
		Action: MergeActionNew{Source: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}},
	})
	if err == nil {
		t.Fatal("expected error for merge during unresolved conflict")
	}

	// can't continue merge with unresolved conflicts
	_, err = repo.Merge(MergeInput{Kind: MergeKindFull, Action: MergeActionCont{}})
	if err == nil {
		t.Fatal("expected error for continue with unresolved conflicts")
	}

	// resolve conflict and continue
	if err := repo.Add([]string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	result2, err := repo.Merge(MergeInput{Kind: MergeKindFull, Action: MergeActionCont{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result2.Result.(MergeResultSuccess); !ok {
		t.Fatalf("expected success, got %v", result2)
	}

	// merging foo again does nothing
	result3 := mergeFoo(t, repo)
	if _, ok := result3.Result.(MergeResultNothing); !ok {
		t.Fatalf("expected nothing, got %v", result3)
	}
}

func TestMergeConflictDeleteModify(t *testing.T) {
	repo := initTestRepo(t)

	// target deletes, source modifies

	addFile(t, repo, "f.txt", "1")
	if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Remove([]string{"f.txt"}, RemoveOptions{UpdateWorkDir: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")
	addFile(t, repo, "f.txt", "2")
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "master")

	result := mergeFoo(t, repo)
	if _, ok := result.Result.(MergeResultConflict); !ok {
		t.Fatalf("expected conflict, got %v", result)
	}

	// can't merge again with an unresolved merge
	_, err := repo.Merge(MergeInput{
		Kind:   MergeKindFull,
		Action: MergeActionNew{Source: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}},
	})
	if err == nil {
		t.Fatal("expected error for merge during unresolved conflict")
	}

	// can't continue merge with unresolved conflicts
	_, err = repo.Merge(MergeInput{Kind: MergeKindFull, Action: MergeActionCont{}})
	if err == nil {
		t.Fatal("expected error for continue with unresolved conflicts")
	}

	// resolve conflict and continue
	if err := repo.Add([]string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	result2, err := repo.Merge(MergeInput{Kind: MergeKindFull, Action: MergeActionCont{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result2.Result.(MergeResultSuccess); !ok {
		t.Fatalf("expected success, got %v", result2)
	}

	// merging foo again does nothing
	result3 := mergeFoo(t, repo)
	if _, ok := result3.Result.(MergeResultNothing); !ok {
		t.Fatalf("expected nothing, got %v", result3)
	}
}

func TestMergeConflictFileDir(t *testing.T) {
	repo := initTestRepo(t)

	// target has file f.txt, source has dir f.txt/

	addFile(t, repo, "hi.txt", "hi")
	if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "f.txt", "hi")
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")
	addFile(t, repo, "f.txt/g.txt", "hi")
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "master")

	result := mergeFoo(t, repo)
	if _, ok := result.Result.(MergeResultConflict); !ok {
		t.Fatalf("expected conflict, got %v", result)
	}

	// make sure renamed file exists
	if _, err := os.Stat(filepath.Join(repo.workPath, "f.txt~master")); err != nil {
		t.Fatalf("expected renamed file f.txt~master: %v", err)
	}

	// can't merge again with an unresolved merge
	_, err := repo.Merge(MergeInput{
		Kind:   MergeKindFull,
		Action: MergeActionNew{Source: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}},
	})
	if err == nil {
		t.Fatal("expected error for merge during unresolved conflict")
	}

	// resolve conflict and continue
	if err := repo.Add([]string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	result2, err := repo.Merge(MergeInput{Kind: MergeKindFull, Action: MergeActionCont{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result2.Result.(MergeResultSuccess); !ok {
		t.Fatalf("expected success, got %v", result2)
	}

	// merging foo again does nothing
	result3 := mergeFoo(t, repo)
	if _, ok := result3.Result.(MergeResultNothing); !ok {
		t.Fatalf("expected nothing, got %v", result3)
	}
}

func TestMergeConflictDirFile(t *testing.T) {
	repo := initTestRepo(t)

	// target has dir f.txt/, source has file f.txt

	addFile(t, repo, "hi.txt", "hi")
	if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "f.txt/g.txt", "hi")
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")
	addFile(t, repo, "f.txt", "hi")
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "master")

	result := mergeFoo(t, repo)
	if _, ok := result.Result.(MergeResultConflict); !ok {
		t.Fatalf("expected conflict, got %v", result)
	}

	// make sure renamed file exists
	if _, err := os.Stat(filepath.Join(repo.workPath, "f.txt~foo")); err != nil {
		t.Fatalf("expected renamed file f.txt~foo: %v", err)
	}

	// can't merge again with an unresolved merge
	_, err := repo.Merge(MergeInput{
		Kind:   MergeKindFull,
		Action: MergeActionNew{Source: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}},
	})
	if err == nil {
		t.Fatal("expected error for merge during unresolved conflict")
	}

	// resolve conflict and continue
	if err := repo.Add([]string{"f.txt"}); err != nil {
		t.Fatal(err)
	}
	result2, err := repo.Merge(MergeInput{Kind: MergeKindFull, Action: MergeActionCont{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result2.Result.(MergeResultSuccess); !ok {
		t.Fatalf("expected success, got %v", result2)
	}

	// merging foo again does nothing
	result3 := mergeFoo(t, repo)
	if _, ok := result3.Result.(MergeResultNothing); !ok {
		t.Fatalf("expected nothing, got %v", result3)
	}
}

func TestMergeConflictBinary(t *testing.T) {
	repo := initTestRepo(t)

	// A --- B --------- D [master]
	//  \               /
	//   \             /
	//    C ---------- [foo]

	// create binary content: alternating bytes and newlines
	var bin [256]byte
	for i := range bin {
		if i%2 == 1 {
			bin[i] = '\n'
		} else {
			bin[i] = byte(i % 255)
		}
	}

	addFile(t, repo, "bin", string(bin[:]))
	if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
		t.Fatal(err)
	}

	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")

	bin[0] = 1
	addFile(t, repo, "bin", string(bin[:]))
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}

	switchBranch(t, repo, "master")

	bin[0] = 2
	addFile(t, repo, "bin", string(bin[:]))
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}

	result := mergeFoo(t, repo)
	if _, ok := result.Result.(MergeResultConflict); !ok {
		t.Fatalf("expected conflict, got %v", result)
	}

	// verify no lines are longer than one byte
	// so we know that conflict markers haven't been added
	content := readWorkFile(t, repo, "bin")
	for _, line := range strings.Split(content, "\n") {
		if len(line) > 1 {
			t.Fatalf("expected no lines longer than 1 byte (no conflict markers in binary), got line of length %d", len(line))
		}
	}

	// resolve conflict and continue
	if err := repo.Add([]string{"bin"}); err != nil {
		t.Fatal(err)
	}
	result2, err := repo.Merge(MergeInput{Kind: MergeKindFull, Action: MergeActionCont{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result2.Result.(MergeResultSuccess); !ok {
		t.Fatalf("expected success, got %v", result2)
	}

	// merging foo again does nothing
	result3 := mergeFoo(t, repo)
	if _, ok := result3.Result.(MergeResultNothing); !ok {
		t.Fatalf("expected nothing, got %v", result3)
	}
}

// demonstrates an example of git shuffling lines unexpectedly
// when auto-resolving a merge conflict
func TestMergeConflictShuffle(t *testing.T) {
	// from https://pijul.org/manual/why_pijul.html
	t.Run("simple", func(t *testing.T) {
		repo := initTestRepo(t)

		// A --- B --- C --- E [master]
		//  \               /
		//   \             /
		//    `---------- D [foo]

		addFile(t, repo, "f.txt", "a\nb")
		if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
			t.Fatal(err)
		}
		if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
			t.Fatal(err)
		}
		addFile(t, repo, "f.txt", "g\na\nb")
		if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
			t.Fatal(err)
		}
		addFile(t, repo, "f.txt", "a\nb\ng\na\nb")
		if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
			t.Fatal(err)
		}
		switchBranch(t, repo, "foo")
		addFile(t, repo, "f.txt", "a\nx\nb")
		if _, err := repo.Commit(CommitMetadata{Message: "d"}); err != nil {
			t.Fatal(err)
		}
		switchBranch(t, repo, "master")

		result := mergeFoo(t, repo)
		if _, ok := result.Result.(MergeResultSuccess); !ok {
			t.Fatalf("expected success, got %v", result)
		}

		// git shuffles lines
		content := readWorkFile(t, repo, "f.txt")
		expected := "a\nx\nb\ng\na\nb"
		if content != expected {
			t.Fatalf("expected %q, got %q", expected, content)
		}

		// merging foo again does nothing
		result2 := mergeFoo(t, repo)
		if _, ok := result2.Result.(MergeResultNothing); !ok {
			t.Fatalf("expected nothing, got %v", result2)
		}
	})

	// from https://tahoe-lafs.org/~zooko/badmerge/concrete-good-semantics.html
	t.Run("concrete", func(t *testing.T) {
		repo := initTestRepo(t)

		// A --- B --- C --- E [master]
		//  \               /
		//   \             /
		//    `---------- D [foo]

		commitA := `int square(int x) {
  int y = x;
  /* Update y to equal the result. */
  /* Question: what is the order of magnitude of this algorithm with respect to x? */
  for (int i = 0; i < x; i++) y += x;
  return y;
}`
		commitB := `int very_slow_square(int x) {
  int y = 0;
  /* Update y to equal the result. */
  /* Question: what is the order of magnitude of this algorithm with respect to x? */
  for (int i = 0; i < x; i++)
    for (int j = 0; j < x; j++)
      y += 1;
  return y;
}

int square(int x) {
  int y = x;
  /* Update y to equal the result. */
  /* Question: what is the order of magnitude of this algorithm with respect to x? */
  for (int i = 0; i < x; i++) y += x;
  return y;
}`
		commitC := `int square(int x) {
  int y = x;
  /* Update y to equal the result. */
  /* Question: what is the order of magnitude of this algorithm with respect to x? */
  return y * x;
}

int very_slow_square(int x) {
  int y = 0;
  /* Update y to equal the result. */
  /* Question: what is the order of magnitude of this algorithm with respect to x? */
  for (int i = 0; i < x; i++)
    for (int j = 0; j < x; j++)
      y += 1;
  return y;
}

int slow_square(int x) {
  int y = x;
  /* Update y to equal the result. */
  /* Question: what is the order of magnitude of this algorithm with respect to x? */
  for (int i = 0; i < x; i++) y += x;
  return y;
}`
		commitD := `int square(int x) {
  int y = 0;
  /* Update y to equal the result. */
  /* Question: what is the order of magnitude of this algorithm with respect to x? */
  for (int i = 0; i < x; i++) y += x;
  return y;
}`

		addFile(t, repo, "f.txt", commitA)
		if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
			t.Fatal(err)
		}
		if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
			t.Fatal(err)
		}
		addFile(t, repo, "f.txt", commitB)
		if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
			t.Fatal(err)
		}
		addFile(t, repo, "f.txt", commitC)
		if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
			t.Fatal(err)
		}
		switchBranch(t, repo, "foo")
		addFile(t, repo, "f.txt", commitD)
		if _, err := repo.Commit(CommitMetadata{Message: "d"}); err != nil {
			t.Fatal(err)
		}
		switchBranch(t, repo, "master")

		mergeFoo(t, repo)

		// git backend expected result
		content := readWorkFile(t, repo, "f.txt")
		expected := `int square(int x) {
  int y = 0;
  /* Update y to equal the result. */
  /* Question: what is the order of magnitude of this algorithm with respect to x? */
  return y * x;
}

int very_slow_square(int x) {
  int y = 0;
  /* Update y to equal the result. */
  /* Question: what is the order of magnitude of this algorithm with respect to x? */
  for (int i = 0; i < x; i++)
    for (int j = 0; j < x; j++)
      y += 1;
  return y;
}

int slow_square(int x) {
  int y = x;
  /* Update y to equal the result. */
  /* Question: what is the order of magnitude of this algorithm with respect to x? */
  for (int i = 0; i < x; i++) y += x;
  return y;
}`
		if content != expected {
			t.Fatalf("unexpected merge result.\nexpected:\n%s\ngot:\n%s", expected, content)
		}
	})
}

func TestCherryPick(t *testing.T) {
	repo := initTestRepo(t)

	// A --- B ------------ D' [master]
	//        \
	//         \
	//          C --- D --- E [foo]

	addFile(t, repo, "readme.md", "a")
	if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "readme.md", "b")
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")

	// commit c modifies a different file, so it shouldn't cause a conflict
	addFile(t, repo, "stuff.md", "c")
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "readme.md", "d")
	commitD, err := repo.Commit(CommitMetadata{Message: "d"})
	if err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "readme.md", "e")
	if _, err := repo.Commit(CommitMetadata{Message: "e"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "master")

	result, err := repo.Merge(MergeInput{
		Kind:   MergeKindPick,
		Action: MergeActionNew{Source: OIDValue{OID: commitD}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Result.(MergeResultSuccess); !ok {
		t.Fatalf("expected success, got %v", result)
	}

	// make sure stuff.md does not exist
	if _, err := os.Stat(filepath.Join(repo.workPath, "stuff.md")); !os.IsNotExist(err) {
		t.Fatal("stuff.md should not exist after cherry-pick")
	}

	// if we try cherry-picking the same commit again, it succeeds again
	result2, err := repo.Merge(MergeInput{
		Kind:     MergeKindPick,
		Action: MergeActionNew{Source: OIDValue{OID: commitD}},
		Metadata: &CommitMetadata{Message: "d", AllowEmpty: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result2.Result.(MergeResultSuccess); !ok {
		t.Fatalf("expected success, got %v", result2)
	}
}

func TestCherryPickConflict(t *testing.T) {
	repo := initTestRepo(t)

	// A --- B ------------ D' [master]
	//        \
	//         \
	//          D --------- E [foo]

	addFile(t, repo, "readme.md", "a")
	if _, err := repo.Commit(CommitMetadata{Message: "a"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "readme.md", "b")
	if _, err := repo.Commit(CommitMetadata{Message: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")
	addFile(t, repo, "readme.md", "c")
	if _, err := repo.Commit(CommitMetadata{Message: "c"}); err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "readme.md", "d")
	commitD, err := repo.Commit(CommitMetadata{Message: "d"})
	if err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "readme.md", "e")
	if _, err := repo.Commit(CommitMetadata{Message: "e"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "master")

	result, err := repo.Merge(MergeInput{
		Kind:   MergeKindPick,
		Action: MergeActionNew{Source: OIDValue{OID: commitD}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Result.(MergeResultConflict); !ok {
		t.Fatalf("expected conflict, got %v", result)
	}

	// verify readme.md has conflict markers
	content := readWorkFile(t, repo, "readme.md")
	if !strings.Contains(content, "<<<<<<< target (master)") {
		t.Fatalf("expected conflict markers, got: %s", content)
	}
	if !strings.Contains(content, ">>>>>>> source (") {
		t.Fatalf("expected source marker, got: %s", content)
	}

	// can't cherry-pick again with an unresolved cherry-pick
	_, err = repo.Merge(MergeInput{
		Kind:   MergeKindPick,
		Action: MergeActionNew{Source: OIDValue{OID: commitD}},
	})
	if err == nil {
		t.Fatal("expected error for cherry-pick during unresolved conflict")
	}

	// can't continue cherry-pick with unresolved conflicts
	_, err = repo.Merge(MergeInput{Kind: MergeKindPick, Action: MergeActionCont{}})
	if err == nil {
		t.Fatal("expected error for continue with unresolved conflicts")
	}

	// resolve conflict
	addFile(t, repo, "readme.md", "e")

	// can't continue with .kind = merge
	_, err = repo.Merge(MergeInput{Kind: MergeKindFull, Action: MergeActionCont{}})
	if err == nil {
		t.Fatal("expected error for continue with wrong merge kind")
	}

	// continue cherry-pick
	result2, err := repo.Merge(MergeInput{Kind: MergeKindPick, Action: MergeActionCont{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result2.Result.(MergeResultSuccess); !ok {
		t.Fatalf("expected success, got %v", result2)
	}
}

func TestLog(t *testing.T) {
	repo := initTestRepo(t)

	// A --- B --- C --------- G --- H [master]
	//        \               /
	//         \             /
	//          D --- E --- F [foo]

	addFile(t, repo, "master.md", "a")
	commitA, err := repo.Commit(CommitMetadata{Message: "a"})
	if err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "master.md", "b")
	commitB, err := repo.Commit(CommitMetadata{Message: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddBranch(AddBranchInput{Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")
	addFile(t, repo, "foo.md", "d")
	commitD, err := repo.Commit(CommitMetadata{Message: "d"})
	if err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "master")
	addFile(t, repo, "master.md", "c")
	commitC, err := repo.Commit(CommitMetadata{Message: "c"})
	if err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "foo")
	addFile(t, repo, "foo.md", "e")
	commitE, err := repo.Commit(CommitMetadata{Message: "e"})
	if err != nil {
		t.Fatal(err)
	}
	addFile(t, repo, "foo.md", "f")
	commitF, err := repo.Commit(CommitMetadata{Message: "f"})
	if err != nil {
		t.Fatal(err)
	}
	switchBranch(t, repo, "master")

	mergeResult, err := repo.Merge(MergeInput{
		Kind:   MergeKindFull,
		Action: MergeActionNew{Source: RefValue{Ref: Ref{Kind: RefHead, Name: "foo"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mergeSuccess2, ok := mergeResult.Result.(MergeResultSuccess)
	if !ok {
		t.Fatalf("expected success, got %v", mergeResult)
	}
	commitG := mergeSuccess2.OID

	addFile(t, repo, "master.md", "h")
	commitH, err := repo.Commit(CommitMetadata{Message: "h"})
	if err != nil {
		t.Fatal(err)
	}

	allOIDs := map[string]bool{
		commitA.Hex(): true, commitB.Hex(): true, commitC.Hex(): true,
		commitD.Hex(): true, commitE.Hex(): true, commitF.Hex(): true,
		commitG.Hex(): true, commitH.Hex(): true,
	}

	// assert that all commits have been found in the log and they aren't repeated
	{
		iter, err := repo.Log(nil)
		if err != nil {
			t.Fatal(err)
		}
		for {
			obj, err := iter.Next()
			if err != nil {
				t.Fatal(err)
			}
			if obj == nil {
				break
			}
			if !allOIDs[obj.OID.Hex()] {
				t.Fatalf("unexpected or repeated commit %s", obj.OID.Hex())
			}
			delete(allOIDs, obj.OID.Hex())
			obj.Close()
		}
		if len(allOIDs) != 0 {
			t.Fatalf("not all commits found in log, remaining: %v", allOIDs)
		}
	}

	// assert that only some commits have been found in the log
	someOIDs := map[string]bool{
		commitC.Hex(): true, commitD.Hex(): true, commitE.Hex(): true,
		commitF.Hex(): true, commitG.Hex(): true,
	}
	{
		iter, err := repo.Log([]Hash{commitG})
		if err != nil {
			t.Fatal(err)
		}
		if err := iter.Exclude(commitB); err != nil {
			t.Fatal(err)
		}
		for {
			obj, err := iter.Next()
			if err != nil {
				t.Fatal(err)
			}
			if obj == nil {
				break
			}
			if !someOIDs[obj.OID.Hex()] {
				t.Fatalf("unexpected or repeated commit %s", obj.OID.Hex())
			}
			delete(someOIDs, obj.OID.Hex())
			obj.Close()
		}
		if len(someOIDs) != 0 {
			t.Fatalf("not all expected commits found, remaining: %v", someOIDs)
		}
	}

	// iterate over all objects
	{
		objIter := repo.NewObjectIterator(ObjectIteratorOptions{Kind: ObjectIterAll, Full: true})
		objIter.Include(commitG)
		count := 0
		for {
			obj, err := objIter.Next()
			if err != nil {
				t.Fatal(err)
			}
			if obj == nil {
				break
			}
			count++
			obj.Close()
		}
		if count != 20 {
			t.Fatalf("expected 20 objects, got %d", count)
		}
	}
}
