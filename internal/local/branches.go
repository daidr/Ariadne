package local

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/daidr/Ariadne/sdk"
)

const MaxShownBytes = 512 * 1024
const SnapshotIndex = "ariadne-snapshot-index"

var seedDirs = map[string]bool{"node_modules": true, "vendor": true, ".pnpm-store": true}

func countValue(value string) *int64 {
	if value == "-" {
		return nil
	}
	count, err := strconv.ParseInt(value, 10, 64)
	if err != nil || count < 0 {
		return nil
	}
	return &count
}
func ParseDiff(nameStatus, numstat string) []sdk.ChangedFile {
	type counts struct{ added, removed *int64 }
	byPath := map[string]counts{}
	stats := strings.Split(numstat, "\x00")
	for i := 0; i < len(stats); i++ {
		if stats[i] == "" {
			continue
		}
		fields := strings.SplitN(stats[i], "\t", 3)
		if len(fields) != 3 {
			continue
		}
		path := fields[2]
		if path == "" {
			if i+2 >= len(stats) {
				break
			}
			i += 2
			path = stats[i]
		}
		byPath[path] = counts{countValue(fields[0]), countValue(fields[1])}
	}
	files := []sdk.ChangedFile{}
	fields := strings.Split(nameStatus, "\x00")
	for i := 0; i < len(fields); i++ {
		if fields[i] == "" {
			continue
		}
		status := map[byte]string{'A': "added", 'M': "modified", 'D': "deleted", 'R': "renamed", 'C': "copied", 'T': "type"}[fields[i][0]]
		if status == "" {
			status = "modified"
		}
		var previous *string
		if status == "renamed" || status == "copied" {
			i++
			if i >= len(fields) {
				break
			}
			previous = new(fields[i])
		}
		i++
		if i >= len(fields) {
			break
		}
		path := fields[i]
		n := byPath[path]
		files = append(files, sdk.ChangedFile{Path: path, PreviousPath: previous, Status: status, Added: n.added, Removed: n.removed})
	}
	return files
}
func ParseStatus(output string) []string {
	paths := []string{}
	fields := strings.Split(output, "\x00")
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 {
			continue
		}
		paths = append(paths, entry[3:])
		if entry[0] == 'R' || entry[0] == 'C' {
			i++
		}
	}
	return paths
}

type BranchInput struct{ Branch, Base, BaseRef, Worktree string }

func branchRevisions(ctx context.Context, git GitRun, branch, baseRef string) (sdk.BranchRevisions, error) {
	head := commitSHA(ctx, git, "refs/heads/"+branch, "")
	if head == "" {
		return sdk.BranchRevisions{}, ctx.Err()
	}
	base := commitSHA(ctx, git, baseRef, "")
	return sdk.BranchRevisions{Head: head, BaseHead: base}, ctx.Err()
}

func BranchChanges(ctx context.Context, git GitRun, input BranchInput) (*sdk.BranchChanges, error) {
	revisions, err := branchRevisions(ctx, git, input.Branch, input.BaseRef)
	if err != nil {
		return nil, err
	}
	head, baseSHA := revisions.Head, revisions.BaseHead
	if head == "" {
		return nil, nil
	}
	if baseSHA == "" {
		return nil, &sdk.MergeRefused{Reason: "no-base", Detail: input.Base + " does not exist"}
	}
	mergeBase := git(ctx, []string{"merge-base", baseSHA, head}, GitOptions{})
	from := baseSHA
	if mergeBase.ExitCode == 0 {
		from = strings.TrimSpace(mergeBase.Stdout)
	}
	files, err := TreeDiff(ctx, git, from, head)
	if err != nil {
		return nil, err
	}
	log := git(ctx, []string{"log", "-z", "--max-count=100", "--format=%H %s", baseSHA + ".." + head}, GitOptions{Whole: true})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	commits := []sdk.BranchCommit{}
	for _, line := range strings.Split(log.Stdout, "\x00") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		sha, subject, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		commits = append(commits, sdk.BranchCommit{SHA: strings.TrimSpace(sha), Subject: subject})
	}
	uncommitted := []string{}
	if input.Worktree != "" {
		status := git(ctx, []string{"status", "--porcelain=v1", "-z", "--untracked-files=normal"}, GitOptions{Directory: input.Worktree, Whole: true})
		if status.ExitCode == 0 {
			uncommitted = ParseStatus(status.Stdout)
		}
	}
	return &sdk.BranchChanges{Branch: input.Branch, Base: input.Base, From: from, Head: head, BaseHead: baseSHA, Commits: commits, Files: files, Uncommitted: uncommitted}, nil
}
func TreeDiff(ctx context.Context, git GitRun, from, to string) ([]sdk.ChangedFile, error) {
	// Resolve user-supplied revisions before handing them to a command that
	// accepts options; --end-of-options protects leading '-' and '--' names.
	revisions := []string{from, to}
	for i, ref := range revisions {
		result := git(ctx, []string{"rev-parse", "--verify", "--end-of-options", ref + "^{tree}"}, GitOptions{})
		if result.ExitCode != 0 {
			return nil, gitFailure(result, "git rev-parse")
		}
		revisions[i] = strings.TrimSpace(result.Stdout)
	}
	name := git(ctx, []string{"diff", "--name-status", "-z", "-M", revisions[0], revisions[1], "--"}, GitOptions{Whole: true})
	if name.ExitCode != 0 {
		return nil, gitFailure(name, "git diff")
	}
	num := git(ctx, []string{"diff", "--numstat", "-z", "-M", revisions[0], revisions[1], "--"}, GitOptions{Whole: true})
	if num.ExitCode != 0 {
		return nil, gitFailure(num, "git diff")
	}
	return ParseDiff(name.Stdout, num.Stdout), nil
}
func SnapshotRefs(threadID string) string { return "refs/ariadne/turns/" + threadID + "/" }
func validRef(ctx context.Context, git GitRun, ref string) error {
	result := git(ctx, []string{"check-ref-format", ref}, GitOptions{})
	if result.ExitCode != 0 {
		return fmt.Errorf("invalid git ref %q", ref)
	}
	return nil
}
func WorktreeSnapshot(ctx context.Context, git GitRun, cwd, threadID string) (string, error) {
	if err := validRef(ctx, git, SnapshotRefs(threadID)+"snapshot"); err != nil {
		return "", err
	}
	dir := git(ctx, []string{"rev-parse", "--absolute-git-dir"}, GitOptions{Directory: cwd})
	if dir.ExitCode != 0 {
		return "", gitFailure(dir, "git rev-parse")
	}
	options := GitOptions{Directory: cwd, Env: map[string]string{"GIT_INDEX_FILE": filepath.Join(strings.TrimSpace(dir.Stdout), SnapshotIndex)}}
	add := git(ctx, []string{"add", "--all"}, options)
	if add.ExitCode != 0 {
		return "", gitFailure(add, "git add")
	}
	tree := git(ctx, []string{"write-tree"}, options)
	if tree.ExitCode != 0 {
		return "", gitFailure(tree, "git write-tree")
	}
	written := strings.TrimSpace(tree.Stdout)
	kept := git(ctx, []string{"update-ref", SnapshotRefs(threadID) + written, written}, GitOptions{Directory: cwd})
	if kept.ExitCode != 0 {
		return "", gitFailure(kept, "git update-ref")
	}
	return written, nil
}
func DropSnapshots(ctx context.Context, git GitRun, threadID string) error {
	prefix := SnapshotRefs(threadID)
	if err := validRef(ctx, git, prefix+"snapshot"); err != nil {
		return err
	}
	refs := git(ctx, []string{"for-each-ref", "--format=%(refname)", prefix}, GitOptions{Whole: true})
	if refs.ExitCode != 0 {
		return gitFailure(refs, "git for-each-ref")
	}
	for _, ref := range strings.Split(refs.Stdout, "\n") {
		if ref == "" {
			continue
		}
		if !strings.HasPrefix(ref, prefix) {
			return fmt.Errorf("unexpected snapshot ref %q", ref)
		}
		result := git(ctx, []string{"update-ref", "-d", ref}, GitOptions{})
		if result.ExitCode != 0 {
			return gitFailure(result, "git update-ref")
		}
	}
	return nil
}

type VersionInput struct {
	From, Head, Path string
	PreviousPath     *string
}

func FileVersions(ctx context.Context, git GitRun, input VersionInput) (sdk.FileVersions, error) {
	oldPath := input.Path
	if input.PreviousPath != nil {
		oldPath = *input.PreviousPath
	}
	before, large1, err := readBlob(ctx, git, input.From, oldPath)
	if err != nil {
		return sdk.FileVersions{}, err
	}
	after, large2, err := readBlob(ctx, git, input.Head, input.Path)
	if err != nil {
		return sdk.FileVersions{}, err
	}
	if large1 || large2 {
		return sdk.FileVersions{Skipped: new("large")}, nil
	}
	for _, value := range []*string{before, after} {
		if value != nil && binaryText(*value) {
			return sdk.FileVersions{Skipped: new("binary")}, nil
		}
	}
	return sdk.FileVersions{Before: before, After: after}, nil
}
func readBlob(ctx context.Context, git GitRun, revision, path string) (*string, bool, error) {
	clean, err := TreePath(path)
	if err != nil {
		return nil, false, err
	}
	resolved := git(ctx, []string{"rev-parse", "--verify", "--end-of-options", revision + "^{tree}"}, GitOptions{})
	if resolved.ExitCode != 0 {
		return nil, false, nil
	}
	spec := strings.TrimSpace(resolved.Stdout) + ":" + clean
	size := git(ctx, []string{"cat-file", "-s", spec}, GitOptions{})
	if size.ExitCode != 0 {
		return nil, false, nil
	}
	count, err := strconv.ParseInt(strings.TrimSpace(size.Stdout), 10, 64)
	if err != nil || count < 0 {
		return nil, false, fmt.Errorf("invalid git blob size")
	}
	if count > MaxShownBytes {
		return nil, true, nil
	}
	content := git(ctx, []string{"cat-file", "blob", spec}, GitOptions{Whole: true})
	if content.ExitCode != 0 {
		return nil, false, gitFailure(content, "git cat-file")
	}
	// The object is immutable; still enforce the limit for remote GitRun adapters.
	if len(content.Stdout) > 3*MaxShownBytes {
		return nil, true, nil
	}
	return &content.Stdout, false, nil
}

func binaryText(text string) bool {
	units := 0
	for _, r := range text {
		if units >= 8000 {
			return false
		}
		if r == 0 {
			return true
		}
		units++
		if r > 0xffff {
			units++
		}
	}
	return false
}
func BranchMerged(ctx context.Context, git GitRun, branch, baseRef string) bool {
	head := commitSHA(ctx, git, "refs/heads/"+branch, "")
	if head == "" {
		return false
	}
	base := commitSHA(ctx, git, baseRef, "")
	if base == "" {
		return false
	}
	return git(ctx, []string{"merge-base", "--is-ancestor", head, base}, GitOptions{}).ExitCode == 0
}
