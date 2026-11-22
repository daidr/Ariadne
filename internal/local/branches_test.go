package local

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestCatFileBatchLarge64FileBatchDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	const count = 64
	beforeBody := strings.Repeat("before-line-0123456789abcdef\n", 300) // > 8 KiB per blob
	afterBody := strings.Repeat("after-line-fedcba9876543210\n", 300)
	paths := make([]string, count)
	for i := range count {
		rel := "dir/nested/deep/path/for/pipe/pressure/file_" + string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".txt"
		paths[i] = rel
		writeGit(t, root, rel, beforeBody+rel+"\n")
	}
	base := gitCommit(t, git, "base 64 files")
	for _, rel := range paths {
		writeGit(t, root, rel, afterBody+rel+"\n")
	}
	head := gitCommit(t, git, "updated 64 files")

	batch := newCatFileBatch(root)
	defer batch.Close()

	inputs := make([]VersionInput, count)
	for i, rel := range paths {
		inputs[i] = VersionInput{From: base, Head: head, Path: rel}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	results, err := batch.ReadVersions(ctx, git, inputs)
	if err != nil {
		t.Fatalf("large 64-file batch failed: %v", err)
	}
	if len(results) != count {
		t.Fatalf("len(results) = %d, want %d", len(results), count)
	}
	for i, rel := range paths {
		if results[i].Before == nil || *results[i].Before != beforeBody+rel+"\n" {
			t.Fatalf("file %d before mismatch", i)
		}
		if results[i].After == nil || *results[i].After != afterBody+rel+"\n" {
			t.Fatalf("file %d after mismatch", i)
		}
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

func TestWorktreeSnapshotNormalizesCRLFWithGlobalAutocrlf(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[core]\n\tautocrlf = true\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	root, git := initGit(t)
	lines := strings.Repeat("line\n", 40)
	writeGit(t, root, "large.txt", lines)
	head := gitCommit(t, git, "add lf file")
	headTree := gitOK(t, git, "rev-parse", head+"^{tree}")

	// Simulate a shell git merge/checkout under core.autocrlf=true writing CRLF
	// line endings onto disk while the committed blob and index remain LF.
	if err := os.Remove(filepath.Join(root, "large.txt")); err != nil {
		t.Fatal(err)
	}
	checkout := exec.CommandContext(t.Context(), "git", "checkout", "--", "large.txt")
	checkout.Dir = root
	if out, err := checkout.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -- large.txt: %v\n%s", err, string(out))
	}
	raw, err := os.ReadFile(filepath.Join(root, "large.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\r\n") {
		t.Fatalf("expected shell git checkout under core.autocrlf=true to write CRLF")
	}
	// Advance mtime so git cannot rely on stat-cache matching alone.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(root, "large.txt"), future, future); err != nil {
		t.Fatal(err)
	}

	snapshot, err := WorktreeSnapshot(t.Context(), git, root, "thread_crlf")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != headTree {
		diff, _ := TreeDiff(t.Context(), git, head, snapshot)
		t.Fatalf("clean CRLF worktree snapshot differed from HEAD tree: diff=%+v", diff)
	}

	changes, err := BranchChanges(t.Context(), git, BranchInput{Branch: "main", Base: "main", BaseRef: "refs/heads/main", Worktree: root})
	if err != nil || changes == nil {
		t.Fatal(changes, err)
	}
	if len(changes.Uncommitted) != 0 {
		t.Fatalf("expected clean uncommitted list for CRLF-normalized file, got %v", changes.Uncommitted)
	}

	// Modify one line while keeping CRLF line endings: diff should be +1 -0, not +41 -40.
	writeGit(t, root, "large.txt", string(raw)+"added\r\n")
	editedSnapshot, err := WorktreeSnapshot(t.Context(), git, root, "thread_crlf")
	if err != nil {
		t.Fatal(err)
	}
	diff, err := TreeDiff(t.Context(), git, head, editedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 1 || diff[0].Path != "large.txt" || diff[0].Added == nil || *diff[0].Added != 1 || diff[0].Removed == nil || *diff[0].Removed != 0 {
		t.Fatalf("expected +1 -0 for single-line CRLF edit, got %+v", diff)
	}
}

func TestBranchChangesPrefersNewerOriginBaseWhenLocalBaseLags(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	oldMain := gitOK(t, git, "rev-parse", "HEAD")

	// Simulate origin/main advancing ahead of stale local main with a large commit.
	gitOK(t, git, "checkout", "-qb", "upstream-tmp")
	writeGit(t, root, "upstream.txt", strings.Repeat("upstream line\n", 100))
	newOriginMain := gitCommit(t, git, "large upstream commit")
	gitOK(t, git, "update-ref", "refs/remotes/origin/main", newOriginMain)

	// Create feature branch from oldMain, make its own change, and merge origin/main
	// while local refs/heads/main remains behind at oldMain.
	gitOK(t, git, "checkout", "-qb", "ariadne/feature", oldMain)
	gitOK(t, git, "branch", "-D", "upstream-tmp")
	writeGit(t, root, "feature.txt", "feat 1\nfeat 2\n")
	gitCommit(t, git, "feature work")
	gitOK(t, git, "merge", "--no-ff", "-m", "Merge origin/main", "refs/remotes/origin/main")
	head := gitOK(t, git, "rev-parse", "HEAD")

	if got := gitOK(t, git, "rev-parse", "refs/heads/main"); got != oldMain {
		t.Fatalf("expected local main to remain behind at %s, got %s", oldMain, got)
	}

	changes, err := BranchChanges(t.Context(), git, BranchInput{
		Branch:        "ariadne/feature",
		Base:          "main",
		BaseRef:       "refs/heads/main",
		RemoteBaseRef: "refs/remotes/origin/main",
		Worktree:      root,
	})
	if err != nil || changes == nil {
		t.Fatal(changes, err)
	}
	if changes.BaseHead != newOriginMain || changes.From != newOriginMain || changes.Head != head {
		t.Fatalf("expected base/from=%s head=%s, got %+v", newOriginMain, head, changes)
	}
	if len(changes.Files) != 1 || changes.Files[0].Path != "feature.txt" || changes.Files[0].Added == nil || *changes.Files[0].Added != 2 || changes.Files[0].Removed == nil || *changes.Files[0].Removed != 0 {
		t.Fatalf("expected only feature.txt (+2 -0), got %+v", changes.Files)
	}
	revs, err := branchRevisions(t.Context(), git, "ariadne/feature", "refs/heads/main", "refs/remotes/origin/main")
	if err != nil || revs.BaseHead != newOriginMain || revs.Head != head {
		t.Fatalf("unexpected branchRevisions: %+v err=%v", revs, err)
	}

	// If local main diverges with unpushed commits, local main must be preserved.
	gitOK(t, git, "checkout", "-q", "main")
	writeGit(t, root, "local-only.txt", "unpushed\n")
	localAhead := gitCommit(t, git, "local unpushed commit")
	if got := resolveBaseSHA(t.Context(), git, "refs/heads/main", "refs/remotes/origin/main"); got != localAhead {
		t.Fatalf("expected diverged local main %s to be preserved, got %s", localAhead, got)
	}
}
