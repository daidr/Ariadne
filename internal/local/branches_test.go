package local

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func initGit(t *testing.T) (string, GitRun) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	git := NewGit(root, "file")
	gitOK(t, git, "init", "-b", "main")
	gitOK(t, git, "config", "user.name", "Ariadne Test")
	gitOK(t, git, "config", "user.email", "test@example.invalid")
	writeGit(t, root, "file.txt", "base\n")
	writeGit(t, root, "untouched.txt", "kept\n")
	gitCommit(t, git, "base")
	return root, git
}
func gitOK(t *testing.T, git GitRun, args ...string) string {
	t.Helper()
	result := git(t.Context(), args, GitOptions{Whole: true})
	if result.ExitCode != 0 {
		t.Fatalf("git %v: %s", args, result.Stderr)
	}
	return strings.TrimSpace(result.Stdout)
}
func writeGit(t *testing.T, root, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte(value), 0644); err != nil {
		t.Fatal(err)
	}
}
func gitCommit(t *testing.T, git GitRun, message string) string {
	t.Helper()
	gitOK(t, git, "add", "--all")
	gitOK(t, git, "commit", "-qm", message)
	return gitOK(t, git, "rev-parse", "HEAD")
}
func TestBranchChangesVersionsAndSnapshots(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	base := gitOK(t, git, "rev-parse", "HEAD")
	gitOK(t, git, "checkout", "-qb", "ariadne/test")
	gitOK(t, git, "mv", "file.txt", "renamed.txt")
	writeGit(t, root, "renamed.txt", "base\nnew\n")
	writeGit(t, root, "new 文件.txt", "new\n")
	writeGit(t, root, "binary.dat", "one\x00two")
	writeGit(t, root, "unicode-binary.dat", strings.Repeat("中", 3000)+"\x00end")
	head := gitCommit(t, git, "changes")
	writeGit(t, root, "uncommitted.txt", "not in branch\n")
	input := BranchInput{Branch: "ariadne/test", Base: "main", BaseRef: "refs/heads/main", Worktree: root}
	changes, err := BranchChanges(t.Context(), git, input)
	if err != nil || changes == nil {
		t.Fatal(changes, err)
	}
	if changes.From != base || changes.Head != head || changes.BaseHead != base || len(changes.Commits) != 1 || len(changes.Files) != 4 || !reflect.DeepEqual(changes.Uncommitted, []string{"uncommitted.txt"}) {
		t.Fatalf("%+v", changes)
	}
	versions, err := FileVersions(t.Context(), git, VersionInput{From: base, Head: head, Path: "renamed.txt", PreviousPath: new("file.txt")})
	if err != nil || versions.Before == nil || *versions.Before != "base\n" || versions.After == nil || *versions.After != "base\nnew\n" {
		t.Fatal(versions, err)
	}
	binary, err := FileVersions(t.Context(), git, VersionInput{From: base, Head: head, Path: "binary.dat"})
	if err != nil || binary.Skipped == nil || *binary.Skipped != "binary" {
		t.Fatal(binary, err)
	}
	before := gitOK(t, git, "write-tree")
	snapshot, err := WorktreeSnapshot(t.Context(), git, root, "thread_test")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == before || gitOK(t, git, "write-tree") != before || gitOK(t, git, "rev-parse", "HEAD") != head {
		t.Fatal("snapshot altered real index or branch")
	}
	content := gitOK(t, git, "show", snapshot+":uncommitted.txt")
	if content != "not in branch" {
		t.Fatal(content)
	}
	if _, err := WorktreeSnapshot(t.Context(), git, root, "../../other"); err == nil {
		t.Fatal("invalid snapshot namespace accepted")
	}
	if err := DropSnapshots(t.Context(), git, "thread_test"); err != nil {
		t.Fatal(err)
	}
	if refs := gitOK(t, git, "for-each-ref", "--format=%(refname)", SnapshotRefs("thread_test")); refs != "" {
		t.Fatal(refs)
	}
	if gitOK(t, git, "rev-parse", "HEAD") != head {
		t.Fatal("branch removed with snapshots")
	}
}
func TestMergeAndUndoKeepOtherDirtyFiles(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"merge", "squash"} {
		t.Run(method, func(t *testing.T) {
			root, git := initGit(t)
			base := gitOK(t, git, "rev-parse", "HEAD")
			gitOK(t, git, "checkout", "-qb", "ariadne/change")
			writeGit(t, root, "file.txt", "changed\n")
			head := gitCommit(t, git, "change")
			gitOK(t, git, "checkout", "-q", "main")
			writeGit(t, root, "untouched.txt", "user change\n")
			out, err := MergeBranch(t.Context(), git, LocalIdentity(root), MergeInput{Branch: "ariadne/change", Base: "main", Method: method, Message: "integrate"})
			if err != nil || out.Kind != "merged" || out.Head != head {
				t.Fatal(out, err)
			}
			if data, _ := os.ReadFile(filepath.Join(root, "file.txt")); string(data) != "changed\n" {
				t.Fatal(string(data))
			}
			parents := strings.Fields(gitOK(t, git, "rev-list", "--parents", "-n", "1", out.SHA))
			if method == "merge" && len(parents) != 3 || method == "squash" && len(parents) != 2 {
				t.Fatal(parents)
			}
			if err := UndoMerge(t.Context(), git, UndoMergeInput{Branch: "ariadne/change", Base: "main", SHA: out.SHA}); err != nil {
				t.Fatal(err)
			}
			if gitOK(t, git, "rev-parse", "HEAD") != base {
				t.Fatal("base not restored")
			}
			if data, _ := os.ReadFile(filepath.Join(root, "untouched.txt")); string(data) != "user change\n" {
				t.Fatal("dirty file lost", string(data))
			}
			if data, _ := os.ReadFile(filepath.Join(root, "file.txt")); string(data) != "base\n" {
				t.Fatal("base files not restored", string(data))
			}
		})
	}
}
func TestMergeRefusalsAndConflictsLeaveBase(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	gitOK(t, git, "checkout", "-qb", "ariadne/change")
	writeGit(t, root, "file.txt", "branch\n")
	gitCommit(t, git, "branch")
	gitOK(t, git, "checkout", "-q", "main")
	base := gitOK(t, git, "rev-parse", "HEAD")
	request := MergeInput{Branch: "ariadne/change", Base: "main", Method: "merge", Message: "merge"}
	stale := request
	stale.ExpectedHead = base
	if out, err := MergeBranch(t.Context(), git, LocalIdentity(root), stale); err != nil || out.Kind != "moved" {
		t.Fatal("merge must leave unreviewed HEAD untouched", out, err)
	}
	if gitOK(t, git, "rev-parse", "HEAD") != base {
		t.Fatal("unreviewed merge moved base")
	}
	writeGit(t, root, "file.txt", "dirty\n")
	_, err := MergeBranch(t.Context(), git, LocalIdentity(root), request)
	var refused *sdk.MergeRefused
	if !errors.As(err, &refused) || refused.Reason != "checkout" {
		t.Fatal(err)
	}
	if gitOK(t, git, "rev-parse", "HEAD") != base {
		t.Fatal("refused merge moved base")
	}
	writeGit(t, root, "file.txt", "main\n")
	base = gitCommit(t, git, "main")
	out, err := MergeBranch(t.Context(), git, LocalIdentity(root), request)
	if err != nil || out.Kind != "conflict" || !reflect.DeepEqual(out.Files, []string{"file.txt"}) {
		t.Fatal(out, err)
	}
	if gitOK(t, git, "rev-parse", "HEAD") != base {
		t.Fatal("conflict moved base")
	}
}
func TestUndoRefusesOtherBranchesAndRemoteRefs(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	gitOK(t, git, "checkout", "-qb", "ariadne/change")
	writeGit(t, root, "file.txt", "changed\n")
	gitCommit(t, git, "change")
	gitOK(t, git, "checkout", "-q", "main")
	out, err := MergeBranch(t.Context(), git, LocalIdentity(root), MergeInput{Branch: "ariadne/change", Base: "main", Method: "squash", Message: "merge"})
	if err != nil {
		t.Fatal(err)
	}
	input := UndoMergeInput{Branch: "ariadne/change", Base: "main", SHA: out.SHA}
	for _, entry := range []struct{ ref, reason string }{{"refs/heads/other", "undo-branched"}, {"refs/remotes/origin/main", "undo-pushed"}} {
		gitOK(t, git, "update-ref", entry.ref, out.SHA)
		err := UndoMerge(t.Context(), git, input)
		var refused *sdk.MergeRefused
		if !errors.As(err, &refused) || refused.Reason != entry.reason {
			t.Fatal(err)
		}
		if gitOK(t, git, "rev-parse", "HEAD") != out.SHA {
			t.Fatal("refused undo changed base")
		}
		gitOK(t, git, "update-ref", "-d", entry.ref)
	}
}
func TestWorktreeRemovalRetainsBranchesAndLocalData(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	dir := filepath.Join(t.TempDir(), "worktree")
	gitOK(t, git, "worktree", "add", "-qb", "ariadne/thread", dir)
	writeGit(t, dir, "local.txt", "local data")
	removed, err := RemoveWorktree(t.Context(), git, dir, RemoveOptions{})
	if err != nil || removed {
		t.Fatal(removed, err)
	}
	if err := os.Remove(filepath.Join(dir, "local.txt")); err != nil {
		t.Fatal(err)
	}
	writeGit(t, dir, ".gitignore", ".env\nnode_modules/\n")
	gitOK(t, git, "-C", dir, "add", ".gitignore")
	gitOK(t, git, "-C", dir, "commit", "-qm", "ignore")
	writeGit(t, dir, ".env", "private")
	removed, err = RemoveWorktree(t.Context(), git, dir, RemoveOptions{Spare: true})
	if err != nil || removed {
		t.Fatal("discarded ignored local data", removed, err)
	}
	removed, err = RemoveWorktree(t.Context(), git, dir, RemoveOptions{Discard: true, Spare: true})
	if err != nil || !removed {
		t.Fatal(removed, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("worktree left behind", err)
	}
	if commitSHA(t.Context(), git, "refs/heads/ariadne/thread", "") == "" {
		t.Fatal("branch removed")
	}
	if _, err := os.Stat(filepath.Join(root, "file.txt")); err != nil {
		t.Fatal("main checkout modified", err)
	}
}

func TestCatFileBatchReadVersions(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	base := gitOK(t, git, "rev-parse", "HEAD")
	gitOK(t, git, "checkout", "-qb", "ariadne/batch")
	gitOK(t, git, "mv", "file.txt", "renamed name.txt")
	writeGit(t, root, "renamed name.txt", "base\nupdated\n")
	writeGit(t, root, "new 文件.txt", "hello world\n")
	writeGit(t, root, "binary.dat", "one\x00two")
	writeGit(t, root, "large.txt", strings.Repeat("a", MaxShownBytes+16))
	if err := os.Remove(filepath.Join(root, "untouched.txt")); err != nil {
		t.Fatal(err)
	}
	head := gitCommit(t, git, "batch changes")

	batch := newCatFileBatch(root)
	defer batch.Close()

	requests := []VersionInput{
		{From: base, Head: head, Path: "renamed name.txt", PreviousPath: new("file.txt")},
		{From: base, Head: head, Path: "new 文件.txt"},
		{From: base, Head: head, Path: "untouched.txt"},
		{From: base, Head: head, Path: "binary.dat"},
		{From: base, Head: head, Path: "large.txt"},
		{From: base, Head: head, Path: "missing.txt"},
	}
	results, err := batch.ReadVersions(t.Context(), git, requests)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(requests) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(requests))
	}
	if results[0].Before == nil || *results[0].Before != "base\n" || results[0].After == nil || *results[0].After != "base\nupdated\n" {
		t.Fatalf("renamed mismatch: %+v", results[0])
	}
	if results[1].Before != nil || results[1].After == nil || *results[1].After != "hello world\n" {
		t.Fatalf("added mismatch: %+v", results[1])
	}
	if results[2].Before == nil || *results[2].Before != "kept\n" || results[2].After != nil {
		t.Fatalf("deleted mismatch: %+v", results[2])
	}
	if results[3].Skipped == nil || *results[3].Skipped != "binary" {
		t.Fatalf("binary mismatch: %+v", results[3])
	}
	if results[4].Skipped == nil || *results[4].Skipped != "large" {
		t.Fatalf("large mismatch: %+v", results[4])
	}
	if results[5].Before != nil || results[5].After != nil || results[5].Skipped != nil {
		t.Fatalf("missing mismatch: %+v", results[5])
	}

	// Verify reuse of the same running process sees newly committed blobs.
	writeGit(t, root, "after-start.txt", "fresh commit\n")
	head2 := gitCommit(t, git, "second commit")
	second, err := batch.ReadVersions(t.Context(), git, []VersionInput{
		{From: head, Head: head2, Path: "after-start.txt"},
	})
	if err != nil || len(second) != 1 || second[0].After == nil || *second[0].After != "fresh commit\n" {
		t.Fatalf("fresh commit mismatch: %+v, err=%v", second, err)
	}
}

func BenchmarkFileVersions40Files(b *testing.B) {
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("git unavailable")
	}
	root := b.TempDir()
	git := NewGit(root, "file")
	run := func(args ...string) string {
		res := git(b.Context(), args, GitOptions{Whole: true})
		if res.ExitCode != 0 {
			b.Fatalf("git %v: %s", args, res.Stderr)
		}
		return strings.TrimSpace(res.Stdout)
	}
	run("init", "-b", "main")
	run("config", "user.name", "Bench")
	run("config", "user.email", "bench@example.invalid")
	const count = 40
	for i := range count {
		p := filepath.Join(root, "pkg", strings.Repeat("a", 1+i%3), "file_"+string(rune('a'+i/26))+string(rune('a'+i%26))+".txt")
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Repeat("before line\n", 40)), 0644); err != nil {
			b.Fatal(err)
		}
	}
	run("add", "--all")
	run("commit", "-qm", "base")
	base := run("rev-parse", "HEAD")

	inputs := make([]VersionInput, 0, count)
	for i := range count {
		rel := filepath.ToSlash(filepath.Join("pkg", strings.Repeat("a", 1+i%3), "file_"+string(rune('a'+i/26))+string(rune('a'+i%26))+".txt"))
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.WriteFile(p, []byte(strings.Repeat("after line\n", 45)), 0644); err != nil {
			b.Fatal(err)
		}
		inputs = append(inputs, VersionInput{From: base, Path: rel})
	}
	run("add", "--all")
	run("commit", "-qm", "changes")
	head := run("rev-parse", "HEAD")
	for i := range inputs {
		inputs[i].Head = head
	}

	b.Run("LegacySequentialPerFile", func(b *testing.B) {
		for b.Loop() {
			for _, in := range inputs {
				if _, err := FileVersions(b.Context(), git, in); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("CatFileBatch", func(b *testing.B) {
		batch := newCatFileBatch(root)
		defer batch.Close()
		// Warm up the persistent reader once before timing steady-state batches.
		if _, err := batch.ReadVersions(b.Context(), git, inputs[:1]); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for b.Loop() {
			if _, err := batch.ReadVersions(b.Context(), git, inputs); err != nil {
				b.Fatal(err)
			}
		}
	})
}
