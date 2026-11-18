package local

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

type TreeView struct {
	git           GitRun
	bytes         GitBytes
	revision, ref string
}

func NewTreeView(git GitRun, bytes GitBytes, revision, ref string) *TreeView {
	return &TreeView{git: git, bytes: bytes, revision: revision, ref: ref}
}
func (view *TreeView) Ref() string { return view.ref }
func (view *TreeView) revisionTree(ctx context.Context) (string, error) {
	result := view.git(ctx, []string{"rev-parse", "--verify", "--end-of-options", view.revision + "^{tree}"}, GitOptions{})
	if result.ExitCode != 0 {
		return "", gitFailure(result, "git rev-parse")
	}
	return strings.TrimSpace(result.Stdout), nil
}
func (view *TreeView) spec(ctx context.Context, path string) (string, error) {
	clean, err := TreePath(path)
	if err != nil {
		return "", err
	}
	tree, err := view.revisionTree(ctx)
	if err != nil {
		return "", err
	}
	return tree + ":" + clean, nil
}
func (view *TreeView) assertType(ctx context.Context, path, spec, want string) error {
	result := view.git(ctx, []string{"cat-file", "-t", spec}, GitOptions{})
	if err := ctx.Err(); err != nil {
		return err
	}
	if result.ExitCode != 0 {
		if path == "" {
			path = "."
		}
		return fmt.Errorf("%s does not exist in %s", path, view.ref)
	}
	if strings.TrimSpace(result.Stdout) != want {
		kind := "a file"
		if want == "tree" {
			kind = "a directory"
		}
		return fmt.Errorf("%s is not %s", path, kind)
	}
	return nil
}
func (view *TreeView) ReadBytes(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 || maxBytes > MaxTextBytes {
		return nil, fmt.Errorf("git byte limit must be between 0 and 64 MiB")
	}
	spec, err := view.spec(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := view.assertType(ctx, path, spec, "blob"); err != nil {
		return nil, err
	}
	size := view.git(ctx, []string{"cat-file", "-s", spec}, GitOptions{})
	if size.ExitCode != 0 {
		return nil, gitFailure(size, "git cat-file")
	}
	count, err := strconv.ParseInt(strings.TrimSpace(size.Stdout), 10, 64)
	if err != nil || count < 0 {
		return nil, fmt.Errorf("invalid git blob size")
	}
	if count > maxBytes {
		return nil, &sdk.FileTooLargeError{Path: path, Size: count, Max: maxBytes}
	}
	data, err := view.bytes(ctx, []string{"cat-file", "blob", spec}, maxBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, &sdk.FileTooLargeError{Path: path, Size: int64(len(data)), Max: maxBytes}
	}
	return data, nil
}
func ParseTree(text string) []sdk.DirectoryEntry {
	entries := []sdk.DirectoryEntry{}
	for _, entry := range strings.Split(text, "\x00") {
		if entry == "" {
			continue
		}
		header, name, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(header)
		if len(fields) < 2 {
			continue
		}
		kind := "directory"
		if fields[1] == "blob" {
			kind = "file"
		}
		entries = append(entries, sdk.DirectoryEntry{Name: name, Kind: kind})
	}
	compare := nameComparator()
	sort.SliceStable(entries, func(i, j int) bool { return compare(entries[i].Name, entries[j].Name) < 0 })
	return entries
}
func (view *TreeView) List(ctx context.Context, path string) ([]sdk.DirectoryEntry, error) {
	spec, err := view.spec(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := view.assertType(ctx, path, spec, "tree"); err != nil {
		return nil, err
	}
	result := view.git(ctx, []string{"ls-tree", "-z", spec}, GitOptions{Whole: true})
	if result.ExitCode != 0 {
		return nil, gitFailure(result, "git ls-tree")
	}
	return ParseTree(result.Stdout), nil
}
func (view *TreeView) Files(ctx context.Context, path string) (sdk.FileListing, error) {
	under, err := TreePath(path)
	if err != nil {
		return sdk.FileListing{}, err
	}
	rev, err := view.revisionTree(ctx)
	if err != nil {
		return sdk.FileListing{}, err
	}
	args := []string{"--literal-pathspecs", "ls-tree", "-r", "-z", "--name-only", rev}
	if under != "" {
		args = append(args, "--", under)
	}
	result := view.git(ctx, args, GitOptions{Whole: true})
	if result.ExitCode != 0 {
		return sdk.FileListing{}, gitFailure(result, "git ls-tree")
	}
	files := []string{}
	for _, file := range strings.Split(result.Stdout, "\x00") {
		if file != "" {
			files = append(files, file)
		}
	}
	return sdk.FileListing{Files: files}, nil
}
func (view *TreeView) Search(ctx context.Context, pattern, path string, options sdk.SearchOptions) (sdk.FileSearchResult, error) {
	under, err := TreePath(path)
	if err != nil {
		return sdk.FileSearchResult{}, err
	}
	keep, err := sdk.SearchFilter(options.Glob)
	if err != nil {
		return sdk.FileSearchResult{}, err
	}
	rev, err := view.revisionTree(ctx)
	if err != nil {
		return sdk.FileSearchResult{}, err
	}
	args := []string{"--literal-pathspecs", "grep", "-n", "-I", "-E"}
	if options.IgnoreCase {
		args = append(args, "-i")
	}
	args = append(args, "--no-color", "--max-count", "50", "-e", pattern, rev)
	if under != "" {
		args = append(args, "--", under)
	}
	result := view.git(ctx, args, GitOptions{Whole: true, Timeout: 30 * time.Second})
	if result.ExitCode != 0 && result.ExitCode != 1 {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			message = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return sdk.FileSearchResult{Lines: []string{}, Error: &message}, nil
	}
	lines := []string{}
	for _, raw := range strings.Split(result.Stdout, "\n") {
		if raw == "" {
			continue
		}
		line := strings.TrimPrefix(raw, rev+":")
		if keep(line) {
			lines = append(lines, line)
		}
	}
	return sdk.FileSearchResult{Lines: sdk.CappedMatches(lines)}, nil
}
