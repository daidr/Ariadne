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
	tpl := ensureBaseGitTemplate(t)
	root := t.TempDir()
	copyTree(t, tpl, root)
	return root, NewGit(root, "file")
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
	gitOK(t, git, "-c", "user.name=Ariadne Test", "-c", "user.email=test@example.invalid", "commit", "-qm", message)
	return gitOK(t, git, "rev-parse", "HEAD")
}
func gitCommitOnly(t *testing.T, git GitRun, message string) {
	t.Helper()
	gitOK(t, git, "add", "--all")
	gitOK(t, git, "-c", "user.name=Ariadne Test", "-c", "user.email=test@example.invalid", "commit", "-qm", message)
}
func gitCommitTracked(t *testing.T, git GitRun, message string) {
	t.Helper()
	gitOK(t, git, "-c", "user.name=Ariadne Test", "-c", "user.email=test@example.invalid", "commit", "-qam", message)
}
func TestBranchChangesVersionsAndSnapshots(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	base := readHeadSHA(t, root)
	writeGitRef(t, filepath.Join(root, ".git"), "refs/heads/ariadne/test", base)
	writeGitRef(t, filepath.Join(root, ".git"), "HEAD", "ref: refs/heads/ariadne/test")
	gitOK(t, git, "mv", "file.txt", "renamed.txt")
	writeGit(t, root, "renamed.txt", "base\nnew\n")
	writeGit(t, root, "new 文件.txt", "new\n")
	writeGit(t, root, "binary.dat", "one\x00two")
	writeGit(t, root, "unicode-binary.dat", strings.Repeat("中", 3000)+"\x00end")
	gitCommitOnly(t, git, "changes")
	head := readHeadSHA(t, root)
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
	if snapshot == before || gitOK(t, git, "write-tree") != before || readHeadSHA(t, root) != head {
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
	if readHeadSHA(t, root) != head {
		t.Fatal("branch removed with snapshots")
	}
}
func TestMergeAndUndoKeepOtherDirtyFiles(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	gitDir := filepath.Join(root, ".git")
	base := readHeadSHA(t, root)
	writeGitRef(t, gitDir, "refs/heads/ariadne/change", base)
	writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/ariadne/change")
	writeGit(t, root, "file.txt", "changed\n")
	gitCommitTracked(t, git, "change")
	head := readHeadSHA(t, root)
	gitOK(t, git, "checkout", "-q", "main")
	writeGit(t, root, "untouched.txt", "user change\n")

	for _, method := range []string{"merge", "squash"} {
		out, err := MergeBranch(t.Context(), git, LocalIdentity(root), MergeInput{Branch: "ariadne/change", Base: "main", Method: method, Message: "integrate"})
		if err != nil || out.Kind != "merged" || out.Head != head {
			t.Fatalf("%s: %+v, %v", method, out, err)
		}
		if data, _ := os.ReadFile(filepath.Join(root, "file.txt")); string(data) != "changed\n" {
			t.Fatalf("%s: %q", method, string(data))
		}
		parents := strings.Fields(gitOK(t, git, "rev-list", "--parents", "-n", "1", out.SHA))
		if method == "merge" && len(parents) != 3 || method == "squash" && len(parents) != 2 {
			t.Fatalf("%s: %v", method, parents)
		}
		if method == "squash" {
			// Verify UndoMerge refuses when another branch or remote ref points at the merge commit.
			input := UndoMergeInput{Branch: "ariadne/change", Base: "main", SHA: out.SHA}
			for _, entry := range []struct{ ref, reason string }{{"refs/heads/other", "undo-branched"}, {"refs/remotes/origin/main", "undo-pushed"}} {
				writeGitRef(t, gitDir, entry.ref, out.SHA)
				err := UndoMerge(t.Context(), git, input)
				var refused *sdk.MergeRefused
				if !errors.As(err, &refused) || refused.Reason != entry.reason {
					t.Fatal(err)
				}
				if readHeadSHA(t, root) != out.SHA {
					t.Fatal("refused undo changed base")
				}
				removeGitRef(t, gitDir, entry.ref)
			}
		}
		if err := UndoMerge(t.Context(), git, UndoMergeInput{Branch: "ariadne/change", Base: "main", SHA: out.SHA}); err != nil {
			t.Fatalf("%s undo: %v", method, err)
		}
		if readHeadSHA(t, root) != base {
			t.Fatalf("%s: base not restored", method)
		}
		if data, _ := os.ReadFile(filepath.Join(root, "untouched.txt")); string(data) != "user change\n" {
			t.Fatalf("%s: dirty file lost: %q", method, string(data))
		}
		if data, _ := os.ReadFile(filepath.Join(root, "file.txt")); string(data) != "base\n" {
			t.Fatalf("%s: base files not restored: %q", method, string(data))
		}
	}
}
func TestMergeRefusalsAndConflictsLeaveBase(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	base := readHeadSHA(t, root)
	writeGitRef(t, filepath.Join(root, ".git"), "refs/heads/ariadne/change", base)
	writeGitRef(t, filepath.Join(root, ".git"), "HEAD", "ref: refs/heads/ariadne/change")
	writeGit(t, root, "file.txt", "branch\n")
	gitCommitTracked(t, git, "branch")
	gitOK(t, git, "checkout", "-q", "main")
	request := MergeInput{Branch: "ariadne/change", Base: "main", Method: "merge", Message: "merge"}
	stale := request
	stale.ExpectedHead = base
	if out, err := MergeBranch(t.Context(), git, LocalIdentity(root), stale); err != nil || out.Kind != "moved" {
		t.Fatal("merge must leave unreviewed HEAD untouched", out, err)
	}
	if readHeadSHA(t, root) != base {
		t.Fatal("unreviewed merge moved base")
	}
	writeGit(t, root, "file.txt", "dirty\n")
	_, err := MergeBranch(t.Context(), git, LocalIdentity(root), request)
	var refused *sdk.MergeRefused
	if !errors.As(err, &refused) || refused.Reason != "checkout" {
		t.Fatal(err)
	}
	if readHeadSHA(t, root) != base {
		t.Fatal("refused merge moved base")
	}
	writeGit(t, root, "file.txt", "main\n")
	gitCommitTracked(t, git, "main")
	base = readHeadSHA(t, root)
	out, err := MergeBranch(t.Context(), git, LocalIdentity(root), request)
	if err != nil || out.Kind != "conflict" || !reflect.DeepEqual(out.Files, []string{"file.txt"}) {
		t.Fatal(out, err)
	}
	if readHeadSHA(t, root) != base {
		t.Fatal("conflict moved base")
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
	base := readHeadSHA(t, root)
	gitDir := filepath.Join(root, ".git")
	writeGitRef(t, gitDir, "refs/heads/ariadne/batch", base)
	writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/ariadne/batch")
	gitOK(t, git, "mv", "file.txt", "renamed name.txt")
	writeGit(t, root, "renamed name.txt", "base\nupdated\n")
	writeGit(t, root, "new 文件.txt", "hello world\n")
	writeGit(t, root, "binary.dat", "one\x00two")
	writeGit(t, root, "large.txt", strings.Repeat("a", MaxShownBytes+16))
	if err := os.Remove(filepath.Join(root, "untouched.txt")); err != nil {
		t.Fatal(err)
	}
	gitCommitOnly(t, git, "batch changes")
	head := readHeadSHA(t, root)

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
	gitCommitOnly(t, git, "second commit")
	head2 := readHeadSHA(t, root)
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
	const uniqueFiles = 16
	const count = 64
	beforeBody := strings.Repeat("before-line-0123456789abcdef\n", 300) // > 8 KiB per blob
	afterBody := strings.Repeat("after-line-fedcba9876543210\n", 300)
	paths := make([]string, uniqueFiles)
	for i := range uniqueFiles {
		rel := "dir/nested/deep/path/for/pipe/pressure/file_" + string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".txt"
		paths[i] = rel
		writeGit(t, root, rel, beforeBody+rel+"\n")
	}
	gitCommitOnly(t, git, "base files")
	base := readHeadSHA(t, root)
	for _, rel := range paths {
		writeGit(t, root, rel, afterBody+rel+"\n")
	}
	gitCommitOnly(t, git, "updated files")
	head := readHeadSHA(t, root)

	batch := newCatFileBatch(root)
	defer batch.Close()

	inputs := make([]VersionInput, count)
	for i := range count {
		inputs[i] = VersionInput{From: base, Head: head, Path: paths[i%uniqueFiles]}
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
	for i := range count {
		rel := paths[i%uniqueFiles]
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
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))

	root, git := initGit(t)
	lines := strings.Repeat("line\n", 40)
	writeGit(t, root, "large.txt", lines)
	gitCommitOnly(t, git, "add lf file")
	head := readHeadSHA(t, root)
	headTree := gitOK(t, git, "rev-parse", head+"^{tree}")

	// Simulate a shell git merge/checkout under core.autocrlf=true writing CRLF
	// line endings onto disk while the committed blob and index remain LF.
	if err := os.Remove(filepath.Join(root, "large.txt")); err != nil {
		t.Fatal(err)
	}
	checkout := exec.CommandContext(t.Context(), gitExecutable(nil), "checkout", "--", "large.txt")
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
	gitDir := filepath.Join(root, ".git")
	oldMain := readHeadSHA(t, root)

	// Simulate origin/main advancing ahead of stale local main with a large commit.
	writeGitRef(t, gitDir, "refs/heads/upstream-tmp", oldMain)
	writeGitRef(t, gitDir, "HEAD", "ref: refs/heads/upstream-tmp")
	writeGit(t, root, "upstream.txt", strings.Repeat("upstream line\n", 100))
	gitCommitOnly(t, git, "large upstream commit")
	newOriginMain := readHeadSHA(t, root)
	writeGitRef(t, gitDir, "refs/remotes/origin/main", newOriginMain)

	// Create feature branch from oldMain, make its own change, and merge origin/main
	// while local refs/heads/main remains behind at oldMain.
	gitOK(t, git, "checkout", "-qb", "ariadne/feature", oldMain)
	removeGitRef(t, gitDir, "refs/heads/upstream-tmp")
	writeGit(t, root, "feature.txt", "feat 1\nfeat 2\n")
	gitCommitOnly(t, git, "feature work")
	gitOK(t, git, "merge", "--no-ff", "-m", "Merge origin/main", "refs/remotes/origin/main")
	head := readHeadSHA(t, root)

	if got := readRefSHA(t, gitDir, "refs/heads/main"); got != oldMain {
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
	localAhead := gitOK(t, git, "commit-tree", oldMain+"^{tree}", "-p", oldMain, "-m", "local unpushed commit")
	writeGitRef(t, gitDir, "refs/heads/main", localAhead)
	if got := resolveBaseSHA(t.Context(), git, "refs/heads/main", "refs/remotes/origin/main"); got != localAhead {
		t.Fatalf("expected diverged local main %s to be preserved, got %s", localAhead, got)
	}
}
