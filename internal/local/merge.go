package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daidr/Ariadne/sdk"
)

type MergeInput struct{ Branch, Base, Method, Message, ExpectedHead string }

func MergeBranch(ctx context.Context, git GitRun, identity Identity, input MergeInput) (sdk.MergeOutcome, error) {
	if input.Method != "squash" && input.Method != "merge" {
		return sdk.MergeOutcome{}, fmt.Errorf("invalid merge method %q", input.Method)
	}
	baseRef := "refs/heads/" + input.Base
	if err := validRef(ctx, git, baseRef); err != nil {
		return sdk.MergeOutcome{}, err
	}
	head := commitSHA(ctx, git, "refs/heads/"+input.Branch, "")
	if head == "" {
		return sdk.MergeOutcome{}, fmt.Errorf("%s does not exist", input.Branch)
	}
	if input.ExpectedHead != "" && head != input.ExpectedHead {
		return sdk.MergeOutcome{Kind: "moved"}, nil
	}
	baseSHA := commitSHA(ctx, git, baseRef, "")
	if baseSHA == "" {
		return sdk.MergeOutcome{}, &sdk.MergeRefused{Reason: "no-base", Detail: input.Base + " does not exist"}
	}
	if git(ctx, []string{"merge-base", "--is-ancestor", head, baseSHA}, GitOptions{}).ExitCode == 0 {
		return sdk.MergeOutcome{Kind: "empty"}, nil
	}
	var author *Author
	var err error
	if identity != nil {
		author, err = identity(ctx)
	}
	if err != nil {
		return sdk.MergeOutcome{}, err
	}
	if author == nil {
		return sdk.MergeOutcome{}, &sdk.MergeRefused{Reason: "identity", Detail: "user.name or user.email is not set"}
	}
	merged := git(ctx, []string{"merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", baseSHA, head}, GitOptions{Whole: true})
	lines := []string{}
	for _, line := range strings.Split(merged.Stdout, "\x00") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	if merged.ExitCode == 1 {
		files := []string{}
		seen := map[string]bool{}
		for _, line := range lines[min(len(lines), 1):] {
			if !seen[line] {
				files = append(files, line)
				seen[line] = true
			}
		}
		return sdk.MergeOutcome{Kind: "conflict", Files: files}, nil
	}
	if merged.ExitCode != 0 {
		message := strings.ToLower(merged.Stderr)
		if strings.Contains(message, "usage") || strings.Contains(message, "unknown option") {
			return sdk.MergeOutcome{}, &sdk.MergeRefused{Reason: "git-version", Detail: strings.TrimSpace(merged.Stderr)}
		}
		return sdk.MergeOutcome{}, gitFailure(merged, "git merge-tree")
	}
	if len(lines) == 0 {
		return sdk.MergeOutcome{}, fmt.Errorf("git merge-tree returned no tree")
	}
	tree := lines[0]
	baseTree := git(ctx, []string{"rev-parse", baseSHA + "^{tree}"}, GitOptions{})
	if tree == strings.TrimSpace(baseTree.Stdout) {
		return sdk.MergeOutcome{Kind: "empty"}, nil
	}
	args := []string{"-c", "user.name=" + author.Name, "-c", "user.email=" + author.Email, "commit-tree", tree, "-p", baseSHA}
	if input.Method == "merge" {
		args = append(args, "-p", head)
	}
	args = append(args, "-m", input.Message)
	commit := git(ctx, args, GitOptions{})
	if commit.ExitCode != 0 {
		return sdk.MergeOutcome{}, gitFailure(commit, "git commit-tree")
	}
	result := strings.TrimSpace(commit.Stdout)
	checkout, err := checkoutOf(ctx, git, baseRef)
	if err != nil {
		return sdk.MergeOutcome{}, err
	}
	if checkout != "" {
		if commitSHA(ctx, git, "HEAD", checkout) != baseSHA {
			return sdk.MergeOutcome{}, &sdk.MergeRefused{Reason: "base-moved", Detail: input.Base + " moved in " + checkout}
		}
		filters, err := usesFilters(ctx, git, tree)
		if err != nil {
			return sdk.MergeOutcome{}, err
		}
		if filters {
			return sdk.MergeOutcome{}, &sdk.MergeRefused{Reason: "filters", Detail: "the repository uses .gitattributes filters"}
		}
		moved := git(ctx, []string{"merge", "--ff-only", "-q", result}, GitOptions{Directory: checkout})
		if moved.ExitCode != 0 {
			return sdk.MergeOutcome{}, &sdk.MergeRefused{Reason: "checkout", Detail: strings.TrimSpace(moved.Stderr)}
		}
	} else {
		moved := git(ctx, []string{"update-ref", baseRef, result, baseSHA}, GitOptions{})
		if moved.ExitCode != 0 {
			return sdk.MergeOutcome{}, &sdk.MergeRefused{Reason: "base-moved", Detail: strings.TrimSpace(moved.Stderr)}
		}
	}
	return sdk.MergeOutcome{Kind: "merged", SHA: result, Head: head}, nil
}

type UndoMergeInput struct{ Branch, Base, SHA string }

func UndoMerge(ctx context.Context, git GitRun, input UndoMergeInput) error {
	baseRef := "refs/heads/" + input.Base
	if err := validRef(ctx, git, baseRef); err != nil {
		return err
	}
	baseSHA := commitSHA(ctx, git, baseRef, "")
	if baseSHA == "" {
		return &sdk.MergeRefused{Reason: "no-base", Detail: input.Base + " does not exist"}
	}
	before := commitSHA(ctx, git, input.SHA+"^1", "")
	if baseSHA != input.SHA || before == "" {
		return &sdk.MergeRefused{Reason: "undo-moved", Detail: input.Base + " is at " + baseSHA[:min(12, len(baseSHA))]}
	}
	holding := git(ctx, []string{"for-each-ref", "--format=%(refname)", "--contains", baseSHA, "refs/remotes", "refs/heads"}, GitOptions{Whole: true})
	if holding.ExitCode != 0 {
		return gitFailure(holding, "git for-each-ref")
	}
	refs := strings.Split(holding.Stdout, "\n")
	for _, ref := range refs {
		if strings.HasPrefix(ref, "refs/remotes/") {
			return &sdk.MergeRefused{Reason: "undo-pushed", Detail: shortRef(ref)}
		}
	}
	for _, ref := range refs {
		if ref != "" && ref != baseRef && ref != "refs/heads/"+input.Branch {
			return &sdk.MergeRefused{Reason: "undo-branched", Detail: shortRef(ref)}
		}
	}
	checkout, err := checkoutOf(ctx, git, baseRef)
	if err != nil {
		return err
	}
	if checkout == "" {
		moved := git(ctx, []string{"update-ref", baseRef, before, baseSHA}, GitOptions{})
		if moved.ExitCode != 0 {
			return &sdk.MergeRefused{Reason: "undo-moved", Detail: strings.TrimSpace(moved.Stderr)}
		}
		return nil
	}
	if commitSHA(ctx, git, "HEAD", checkout) != baseSHA {
		return &sdk.MergeRefused{Reason: "undo-moved", Detail: input.Base + " moved in " + checkout}
	}
	tree := git(ctx, []string{"rev-parse", before + "^{tree}"}, GitOptions{})
	filters, err := usesFilters(ctx, git, strings.TrimSpace(tree.Stdout))
	if err != nil {
		return err
	}
	if filters {
		return &sdk.MergeRefused{Reason: "filters", Detail: "the repository uses .gitattributes filters"}
	}
	moved := git(ctx, []string{"reset", "--keep", "-q", before}, GitOptions{Directory: checkout})
	if moved.ExitCode != 0 {
		return &sdk.MergeRefused{Reason: "checkout", Detail: strings.TrimSpace(moved.Stderr)}
	}
	return nil
}
func shortRef(ref string) string {
	for _, prefix := range []string{"refs/heads/", "refs/remotes/"} {
		if strings.HasPrefix(ref, prefix) {
			return strings.TrimPrefix(ref, prefix)
		}
	}
	return ref
}
func usesFilters(ctx context.Context, git GitRun, tree string) (bool, error) {
	found := git(ctx, []string{"grep", "-q", "-E", "[[:space:]]filter=", tree, "--", ".gitattributes", "**/.gitattributes"}, GitOptions{})
	if found.ExitCode == 0 {
		return true, nil
	}
	if found.ExitCode == 1 {
		return false, nil
	}
	return false, gitFailure(found, "git grep")
}
func worktreeFields(ctx context.Context, git GitRun) ([]string, error) {
	list := git(ctx, []string{"worktree", "list", "--porcelain", "-z"}, GitOptions{Whole: true})
	if list.ExitCode != 0 {
		return nil, gitFailure(list, "git worktree list")
	}
	return strings.Split(list.Stdout, "\x00"), nil
}

func checkoutOf(ctx context.Context, git GitRun, ref string) (string, error) {
	fields, err := worktreeFields(ctx, git)
	if err != nil {
		return "", err
	}
	path := ""
	for _, field := range fields {
		if field == "" {
			path = ""
			continue
		}
		if strings.HasPrefix(field, "worktree ") {
			path = strings.TrimPrefix(field, "worktree ")
		} else if field == "branch "+ref && path != "" {
			return path, nil
		}
	}
	return "", nil
}
func directWorktreeGitDir(directory string) string {
	gitPath := filepath.Join(directory, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	if info.IsDir() {
		return gitPath
	}
	if !info.Mode().IsRegular() {
		return ""
	}
	gitDir, ok := resolveGitFileTarget(directory, gitPath)
	if !ok {
		return ""
	}
	return gitDir
}

func holdsMore(ctx context.Context, git GitRun, directory string) bool {
	options := GitOptions{Directory: directory, Whole: true}
	if git(ctx, []string{"--no-optional-locks", "symbolic-ref", "-q", "HEAD"}, options).ExitCode != 0 {
		return true
	}
	gitDir := directWorktreeGitDir(directory)
	if gitDir != "" {
		if info, err := os.Stat(filepath.Join(gitDir, "reftable")); err == nil && info.IsDir() {
			gitDir = ""
		}
	}
	for _, ref := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if gitDir != "" {
			if _, err := os.Lstat(filepath.Join(gitDir, ref)); os.IsNotExist(err) {
				continue
			}
		}
		if git(ctx, []string{"--no-optional-locks", "rev-parse", "-q", "--verify", ref}, options).ExitCode == 0 {
			return true
		}
	}
	ignored := git(ctx, []string{"--no-optional-locks", "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z"}, options)
	if ignored.ExitCode != 0 {
		return true
	}
	for _, entry := range strings.Split(ignored.Stdout, "\x00") {
		if entry != "" && !(strings.HasSuffix(entry, "/") && seedDirs[filepath.Base(strings.TrimSuffix(entry, "/"))]) {
			return true
		}
	}
	return false
}

type RemoveOptions struct{ Discard, Spare, Merged bool }

func outerWorktreeBlocked(ctx context.Context, git GitRun, directory string) (bool, error) {
	gitDirRes := git(ctx, []string{"rev-parse", "--path-format=absolute", "--git-dir"}, GitOptions{Directory: directory})
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if gitDirRes.ExitCode != 0 {
		return true, nil
	}
	if outerGitDir := strings.TrimSpace(gitDirRes.Stdout); outerGitDir != "" {
		if _, err := os.Lstat(filepath.Join(outerGitDir, "locked")); err == nil || !os.IsNotExist(err) {
			return true, nil
		}
		for _, marker := range inProgressGitMarkers {
			if _, err := os.Lstat(filepath.Join(outerGitDir, marker)); err == nil || !os.IsNotExist(err) {
				return true, nil
			}
		}
	}
	statusArgs := append(worktreeConfigArgs(ctx, directory), "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	status := git(ctx, statusArgs, GitOptions{Directory: directory, Whole: true})
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if status.ExitCode != 0 || status.Stdout != "" {
		return true, nil
	}
	return false, nil
}

func RemoveWorktree(ctx context.Context, git GitRun, directory string, options RemoveOptions) (bool, error) {
	if _, err := os.Lstat(directory); os.IsNotExist(err) {
		pruned := git(ctx, []string{"worktree", "prune"}, GitOptions{})
		if pruned.ExitCode != 0 {
			return false, gitFailure(pruned, "git worktree prune")
		}
		return true, nil
	} else if err != nil {
		return false, err
	}
	if options.Spare && !options.Discard && holdsMore(ctx, git, directory) {
		return false, nil
	}
	embedded, err := DiscoverEmbeddedRepos(ctx, directory)
	if err != nil {
		if !options.Discard || ctx.Err() != nil {
			return false, err
		}
		embedded = nil
	}
	outerHasSubmodule := hasDirectSubmodule(embedded, directory)
	if !options.Discard && len(embedded) > 0 {
		for _, repo := range embedded {
			status, err := InspectEmbeddedRepo(ctx, repo)
			if err != nil {
				return false, err
			}
			if status.Dirty || (repo.ObjectStoreInsideWorktree && status.Unpushed) || (options.Merged && status.Unmerged) {
				return false, nil
			}
		}
		if outerHasSubmodule || len(nestedWorktreesDeepestFirst(embedded)) > 0 {
			blocked, err := outerWorktreeBlocked(ctx, git, directory)
			if err != nil {
				return false, err
			}
			if blocked {
				return false, nil
			}
		}
	}
	if len(embedded) > 0 {
		removedNested, err := removeNestedWorktrees(ctx, embedded, options.Discard)
		if err != nil {
			return false, err
		}
		if !removedNested {
			return false, nil
		}
	}
	args := []string{"worktree", "remove"}
	if options.Discard {
		args = append(args, "--force", "--force")
	} else if outerHasSubmodule {
		args = append(args, "--force")
	}
	args = append(args, "--", directory)
	result := git(ctx, args, GitOptions{})
	if result.ExitCode == 0 {
		return true, nil
	}
	lower := strings.ToLower(result.Stderr)
	if strings.Contains(lower, "modified or untracked") || strings.Contains(lower, "use --force") || strings.Contains(lower, "locked") || strings.Contains(lower, "submodules") {
		return false, nil
	}
	return false, gitFailure(result, "git worktree remove")
}
