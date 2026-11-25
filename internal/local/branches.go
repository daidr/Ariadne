package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

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

// ParseRawNumstat parses the combined output of `git diff --raw --numstat -z -M`.
func ParseRawNumstat(output string) []sdk.ChangedFile {
	tokens := strings.Split(output, "\x00")
	files := []sdk.ChangedFile{}
	i := 0
	for i < len(tokens) {
		entry := tokens[i]
		if entry == "" {
			i++
			continue
		}
		if !strings.HasPrefix(entry, ":") {
			break
		}
		fields := strings.Fields(entry)
		if len(fields) < 5 || len(fields[4]) == 0 {
			i++
			continue
		}
		status := map[byte]string{'A': "added", 'M': "modified", 'D': "deleted", 'R': "renamed", 'C': "copied", 'T': "type"}[fields[4][0]]
		if status == "" {
			status = "modified"
		}
		var previous *string
		if status == "renamed" || status == "copied" {
			i++
			if i >= len(tokens) {
				break
			}
			previous = new(tokens[i])
		}
		i++
		if i >= len(tokens) {
			break
		}
		path := tokens[i]
		i++
		files = append(files, sdk.ChangedFile{Path: path, PreviousPath: previous, Status: status})
	}
	type counts struct{ added, removed *int64 }
	byPath := make(map[string]counts, len(files))
	for ; i < len(tokens); i++ {
		if tokens[i] == "" {
			continue
		}
		fields := strings.SplitN(tokens[i], "\t", 3)
		if len(fields) != 3 {
			continue
		}
		path := fields[2]
		if path == "" {
			if i+2 >= len(tokens) {
				break
			}
			i += 2
			path = tokens[i]
		}
		byPath[path] = counts{countValue(fields[0]), countValue(fields[1])}
	}
	for idx := range files {
		n := byPath[files[idx].Path]
		files[idx].Added = n.added
		files[idx].Removed = n.removed
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

type BranchInput struct{ Branch, Base, BaseRef, RemoteBaseRef, Worktree string }

// resolveBaseSHA picks baseRef, or upgrades to remoteBaseRef when baseRef is a
// strict ancestor of remoteBaseRef (for example, local master lagging behind
// origin/master after the branch merged origin/master). Local unpushed commits
// on baseRef are preserved because baseRef is not an ancestor of remoteBaseRef.
func resolveBaseSHA(ctx context.Context, git GitRun, baseRef, remoteBaseRef string) string {
	baseSHA := commitSHA(ctx, git, baseRef, "")
	if remoteBaseRef == "" {
		return baseSHA
	}
	remoteSHA := commitSHA(ctx, git, remoteBaseRef, "")
	if remoteSHA == "" || remoteSHA == baseSHA {
		return baseSHA
	}
	if baseSHA == "" {
		return remoteSHA
	}
	if git(ctx, []string{"merge-base", "--is-ancestor", baseSHA, remoteSHA}, GitOptions{}).ExitCode == 0 {
		return remoteSHA
	}
	return baseSHA
}

func branchRevisions(ctx context.Context, git GitRun, branch, baseRef, remoteBaseRef string) (sdk.BranchRevisions, error) {
	var head, base string
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		head = commitSHA(ctx, git, "refs/heads/"+branch, "")
	}()
	go func() {
		defer wg.Done()
		base = resolveBaseSHA(ctx, git, baseRef, remoteBaseRef)
	}()
	wg.Wait()
	if head == "" {
		return sdk.BranchRevisions{}, ctx.Err()
	}
	return sdk.BranchRevisions{Head: head, BaseHead: base}, ctx.Err()
}

func BranchChanges(ctx context.Context, git GitRun, input BranchInput) (*sdk.BranchChanges, error) {
	revisions, err := branchRevisions(ctx, git, input.Branch, input.BaseRef, input.RemoteBaseRef)
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
	from := baseSHA
	if head != baseSHA {
		mergeBase := git(ctx, []string{"merge-base", baseSHA, head}, GitOptions{})
		if mergeBase.ExitCode == 0 {
			from = strings.TrimSpace(mergeBase.Stdout)
		}
	}

	var (
		files       = []sdk.ChangedFile{}
		diffErr     error
		logResult   sdk.ExecResult
		uncommitted = []string{}
		wg          sync.WaitGroup
	)
	if from != head {
		wg.Add(1)
		go func() {
			defer wg.Done()
			files, diffErr = TreeDiff(ctx, git, from, head)
		}()
	}
	if head != baseSHA {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logResult = git(ctx, []string{"log", "-z", "--max-count=100", "--format=%H %s", baseSHA + ".." + head}, GitOptions{Whole: true})
		}()
	}
	if input.Worktree != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statusArgs := append(worktreeConfigArgs(ctx, input.Worktree), "status", "--porcelain=v1", "-z", "--untracked-files=normal")
			status := git(ctx, statusArgs, GitOptions{Directory: input.Worktree, Whole: true})
			if status.ExitCode == 0 {
				uncommitted = ParseStatus(status.Stdout)
			}
		}()
	}
	wg.Wait()
	if diffErr != nil {
		return nil, diffErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	commits := []sdk.BranchCommit{}
	for _, line := range strings.Split(logResult.Stdout, "\x00") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		sha, subject, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		commits = append(commits, sdk.BranchCommit{SHA: strings.TrimSpace(sha), Subject: subject})
	}
	return &sdk.BranchChanges{Branch: input.Branch, Base: input.Base, From: from, Head: head, BaseHead: baseSHA, Commits: commits, Files: files, Uncommitted: uncommitted}, nil
}
func TreeDiff(ctx context.Context, git GitRun, from, to string) ([]sdk.ChangedFile, error) {
	if from == to && isObjectID(from) {
		return []sdk.ChangedFile{}, nil
	}
	// Resolve user-supplied revisions before handing them to a command that
	// accepts options; --end-of-options protects leading '-' and '--' names.
	revisions := []string{from, to}
	for i, ref := range revisions {
		if isObjectID(ref) {
			revisions[i] = ref + "^{tree}"
			continue
		}
		result := git(ctx, []string{"rev-parse", "--verify", "--end-of-options", ref + "^{tree}"}, GitOptions{})
		if result.ExitCode != 0 {
			return nil, gitFailure(result, "git rev-parse")
		}
		revisions[i] = strings.TrimSpace(result.Stdout)
	}
	diff := git(ctx, []string{"diff", "--raw", "--numstat", "-z", "-M", revisions[0], revisions[1], "--"}, GitOptions{Whole: true})
	if diff.ExitCode != 0 {
		return nil, gitFailure(diff, "git diff")
	}
	return ParseRawNumstat(diff.Stdout), nil
}
func SnapshotRefs(threadID string) string { return "refs/ariadne/turns/" + threadID + "/" }
func validRef(ctx context.Context, git GitRun, ref string) error {
	result := git(ctx, []string{"check-ref-format", ref}, GitOptions{})
	if result.ExitCode != 0 {
		return fmt.Errorf("invalid git ref %q", ref)
	}
	return nil
}
func seedSnapshotIndex(gitDir, snapIndex string) {
	realIndex := filepath.Join(gitDir, "index")
	data, err := os.ReadFile(realIndex)
	if err != nil {
		if os.IsNotExist(err) {
			_ = os.Remove(snapIndex)
		}
		return
	}
	tmp, err := os.CreateTemp(gitDir, SnapshotIndex+".tmp-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, snapIndex); err != nil {
		_ = os.Remove(tmpName)
		_ = os.WriteFile(snapIndex, data, 0600)
	}
}

func WorktreeSnapshot(ctx context.Context, git GitRun, cwd, threadID string) (string, error) {
	if err := validRef(ctx, git, SnapshotRefs(threadID)+"snapshot"); err != nil {
		return "", err
	}
	dir := git(ctx, []string{"rev-parse", "--absolute-git-dir"}, GitOptions{Directory: cwd})
	if dir.ExitCode != 0 {
		return "", gitFailure(dir, "git rev-parse")
	}
	gitDir := strings.TrimSpace(dir.Stdout)
	snapIndex := filepath.Join(gitDir, SnapshotIndex)
	seedSnapshotIndex(gitDir, snapIndex)
	options := GitOptions{Directory: cwd, Env: map[string]string{"GIT_INDEX_FILE": snapIndex}}
	addArgs := append(worktreeConfigArgs(ctx, cwd), "-c", "core.safecrlf=false", "add", "--all")
	add := git(ctx, addArgs, options)
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
	tree := revision + "^{tree}"
	if !isObjectID(revision) {
		resolved := git(ctx, []string{"rev-parse", "--verify", "--end-of-options", tree}, GitOptions{})
		if resolved.ExitCode != 0 {
			return nil, false, nil
		}
		tree = strings.TrimSpace(resolved.Stdout)
	}
	spec := tree + ":" + clean
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

func isObjectID(ref string) bool {
	return (len(ref) == 40 || len(ref) == 64) && strings.IndexFunc(ref, func(r rune) bool {
		return !strings.ContainsRune("0123456789abcdefABCDEF", r)
	}) == -1
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
	if head == base {
		return true
	}
	return git(ctx, []string{"--no-optional-locks", "merge-base", "--is-ancestor", head, base}, GitOptions{}).ExitCode == 0
}
