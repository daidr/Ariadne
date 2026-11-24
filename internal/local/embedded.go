package local

import (
	"cmp"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/daidr/Ariadne/internal/fsutil"
)

type EmbeddedRepoKind string

const (
	EmbeddedStandalone EmbeddedRepoKind = "standalone"
	EmbeddedWorktree   EmbeddedRepoKind = "worktree"
	EmbeddedGitFile    EmbeddedRepoKind = "gitfile"
)

const (
	maxEmbeddedGitDepth = 8
	maxGitFileBytes     = 8 << 10
)

// Directories that never hold user source repositories and can be huge in real workspaces.
var skipEmbeddedScanDirs = map[string]bool{
	".git":          true,
	"node_modules":  true,
	"vendor":        true,
	".pnpm-store":   true,
	"__pycache__":   true,
	".venv":         true,
	".mypy_cache":   true,
	".pytest_cache": true,
	".ruff_cache":   true,
	".cache":        true,
	".next":         true,
	".nuxt":         true,
	".turbo":        true,
	".parcel-cache": true,
	".gradle":       true,
	".yarn":         true,
}

var inProgressGitMarkers = []string{
	"MERGE_HEAD",
	"CHERRY_PICK_HEAD",
	"REVERT_HEAD",
	"BISECT_LOG",
	"rebase-merge",
	"rebase-apply",
}

type EmbeddedRepo struct {
	Path                      string
	RelPath                   string
	Depth                     int
	Kind                      EmbeddedRepoKind
	GitDir                    string
	CommonDir                 string
	RegisteredWorktreePath    string
	ObjectStoreInsideWorktree bool
}

type EmbeddedRepoStatus struct {
	Repo     EmbeddedRepo
	Dirty    bool
	Unmerged bool
	Unpushed bool
}

// DiscoverEmbeddedRepos finds nested Git repositories inside worktreeDir by
// looking for standard .git directories and gitfiles (excluding worktreeDir's
// own top-level .git). Symlinks are never followed and dependency/cache
// directories are pruned.
func DiscoverEmbeddedRepos(ctx context.Context, worktreeDir string) ([]EmbeddedRepo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := canonicalOrClean(worktreeDir)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}

	var repos []EmbeddedRepo
	var walk func(dir string, depth int) error
	walk = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if depth > 0 {
			for _, entry := range entries {
				if entry.Name() != ".git" {
					continue
				}
				entryType := entry.Type()
				if entryType&os.ModeSymlink != 0 {
					break
				}
				rel, err := filepath.Rel(root, dir)
				if err != nil {
					rel = dir
				}
				gitPath := filepath.Join(dir, ".git")
				if entry.IsDir() {
					repos = append(repos, EmbeddedRepo{
						Path:                      dir,
						RelPath:                   filepath.ToSlash(rel),
						Depth:                     depth,
						Kind:                      EmbeddedStandalone,
						GitDir:                    gitPath,
						CommonDir:                 gitPath,
						ObjectStoreInsideWorktree: true,
					})
				} else if entryType.IsRegular() {
					repos = append(repos, inspectEmbeddedGitFile(ctx, root, dir, filepath.ToSlash(rel), depth, gitPath))
				}
				break
			}
		}
		if depth >= maxEmbeddedGitDepth {
			return nil
		}
		for _, entry := range entries {
			name := entry.Name()
			if skipEmbeddedScanDirs[name] {
				continue
			}
			if !entry.IsDir() {
				continue
			}
			if err := walk(filepath.Join(dir, name), depth+1); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(root, 0); err != nil {
		return nil, err
	}
	slices.SortFunc(repos, func(a, b EmbeddedRepo) int {
		return cmp.Compare(a.RelPath, b.RelPath)
	})
	return repos, nil
}

func canonicalOrClean(path string) (string, error) {
	if resolved, err := fsutil.CanonicalPath(path); err == nil {
		return resolved, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func readSmallTextFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxGitFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxGitFileBytes {
		return "", errors.New("git metadata file too large")
	}
	return string(data), nil
}

func inspectEmbeddedGitFile(ctx context.Context, worktreeRoot, repoDir, relPath string, depth int, gitFile string) EmbeddedRepo {
	repo := EmbeddedRepo{
		Path:    repoDir,
		RelPath: relPath,
		Depth:   depth,
		Kind:    EmbeddedGitFile,
	}
	raw, err := readSmallTextFile(gitFile)
	if err != nil {
		return repo
	}
	line, _, _ := strings.Cut(raw, "\n")
	line = strings.TrimRight(line, "\r")
	target, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return repo
	}
	target = filepath.FromSlash(strings.TrimSpace(target))
	if target == "" {
		return repo
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(repoDir, target)
	}
	gitDir, err := canonicalOrClean(target)
	if err != nil {
		return repo
	}
	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return repo
	}
	repo.GitDir = gitDir
	repo.CommonDir = gitDir

	if commonDir, ok := linkedWorktreeCommonDir(gitDir); ok {
		repo.Kind = EmbeddedWorktree
		repo.CommonDir = commonDir
		repo.RegisteredWorktreePath = lookupRegisteredWorktreePath(ctx, commonDir, repoDir)
	}
	repo.ObjectStoreInsideWorktree = isPathWithin(repo.CommonDir, worktreeRoot)
	return repo
}

func linkedWorktreeCommonDir(gitDir string) (string, bool) {
	gitdirMarker, err := os.Stat(filepath.Join(gitDir, "gitdir"))
	if err != nil || !gitdirMarker.Mode().IsRegular() {
		return "", false
	}
	commonRaw, err := readSmallTextFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return "", false
	}
	commonRel := filepath.FromSlash(strings.TrimSpace(commonRaw))
	if commonRel == "" {
		return "", false
	}
	if !filepath.IsAbs(commonRel) {
		commonRel = filepath.Join(gitDir, commonRel)
	}
	commonDir, err := canonicalOrClean(commonRel)
	if err != nil {
		return "", false
	}
	commonInfo, err := os.Stat(commonDir)
	if err != nil || !commonInfo.IsDir() {
		return "", false
	}
	parentInfo, err := os.Stat(filepath.Dir(gitDir))
	if err != nil || !parentInfo.IsDir() {
		return "", false
	}
	worktreesInfo, err := os.Stat(filepath.Join(commonDir, "worktrees"))
	if err != nil || !worktreesInfo.IsDir() || !os.SameFile(parentInfo, worktreesInfo) {
		return "", false
	}
	return commonDir, true
}

func lookupRegisteredWorktreePath(ctx context.Context, commonDir, repoDir string) string {
	repoInfo, err := os.Lstat(repoDir)
	if err != nil || !repoInfo.IsDir() {
		return ""
	}
	hostGit := NewGit(commonDir, "file")
	list := hostGit(ctx, []string{"--git-dir", commonDir, "worktree", "list", "--porcelain", "-z"}, GitOptions{Directory: commonDir, Whole: true})
	if list.ExitCode != 0 {
		return ""
	}
	for _, field := range strings.Split(list.Stdout, "\x00") {
		path, ok := strings.CutPrefix(field, "worktree ")
		if !ok || path == "" {
			continue
		}
		info, err := os.Lstat(path)
		if err == nil && info.IsDir() && os.SameFile(repoInfo, info) {
			return path
		}
	}
	return ""
}

func isPathWithin(target, root string) bool {
	if target == "" || root == "" {
		return false
	}
	resolvedTarget, err := canonicalOrClean(target)
	if err != nil {
		return false
	}
	resolvedRoot, err := canonicalOrClean(root)
	if err != nil {
		return false
	}
	return within(cleanPathKey(resolvedTarget), cleanPathKey(resolvedRoot))
}

// InspectEmbeddedRepo evaluates whether an embedded repository has uncommitted
// work, in-progress Git operations, unpushed commits in an in-worktree object
// store, or commits not yet merged into its base/default branch.
func InspectEmbeddedRepo(ctx context.Context, repo EmbeddedRepo) (EmbeddedRepoStatus, error) {
	if err := ctx.Err(); err != nil {
		return EmbeddedRepoStatus{}, err
	}
	status := EmbeddedRepoStatus{Repo: repo}
	if repo.GitDir == "" || (repo.Kind == EmbeddedWorktree && repo.RegisteredWorktreePath == "") {
		status.Dirty = true
		status.Unmerged = true
		status.Unpushed = repo.ObjectStoreInsideWorktree
		return status, nil
	}
	for _, marker := range inProgressGitMarkers {
		if _, err := os.Lstat(filepath.Join(repo.GitDir, marker)); err == nil || !os.IsNotExist(err) {
			status.Dirty = true
			break
		}
	}

	repoGit := NewGit(repo.Path, "file")
	sym := repoGit(ctx, []string{"symbolic-ref", "-q", "HEAD"}, GitOptions{})
	if err := ctx.Err(); err != nil {
		return EmbeddedRepoStatus{}, err
	}
	branchRef := ""
	if sym.ExitCode != 0 {
		status.Dirty = true
	} else {
		branchRef = strings.TrimSpace(sym.Stdout)
	}

	statusArgs := append(worktreeConfigArgs(ctx, repo.Path), "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	st := repoGit(ctx, statusArgs, GitOptions{Whole: true})
	if err := ctx.Err(); err != nil {
		return EmbeddedRepoStatus{}, err
	}
	if st.ExitCode != 0 || st.Stdout != "" {
		status.Dirty = true
	}

	headSHA := commitSHA(ctx, repoGit, "HEAD", "")
	if err := ctx.Err(); err != nil {
		return EmbeddedRepoStatus{}, err
	}

	if repo.ObjectStoreInsideWorktree {
		revArgs := []string{"rev-list"}
		if headSHA != "" {
			revArgs = append(revArgs, "HEAD")
		}
		revArgs = append(revArgs, "--branches", "--not", "--remotes", "--max-count=1")
		unpushed := repoGit(ctx, revArgs, GitOptions{})
		if err := ctx.Err(); err != nil {
			return EmbeddedRepoStatus{}, err
		}
		if unpushed.ExitCode != 0 || strings.TrimSpace(unpushed.Stdout) != "" {
			status.Unpushed = true
		} else if repoGit(ctx, []string{"rev-parse", "--verify", "--quiet", "refs/stash"}, GitOptions{}).ExitCode == 0 {
			status.Unpushed = true
		}
		if err := ctx.Err(); err != nil {
			return EmbeddedRepoStatus{}, err
		}
	}

	switch {
	case headSHA == "":
		status.Unmerged = false
	case repo.ObjectStoreInsideWorktree && status.Unpushed:
		status.Unmerged = true
	case branchRef == "":
		status.Unmerged = true
	default:
		status.Unmerged = !embeddedBranchMerged(ctx, repoGit, repo, branchRef, headSHA)
	}
	if err := ctx.Err(); err != nil {
		return EmbeddedRepoStatus{}, err
	}
	return status, nil
}

func embeddedBranchMerged(ctx context.Context, git GitRun, repo EmbeddedRepo, branchRef, headSHA string) bool {
	isAncestor := func(targetRef string) bool {
		if targetRef == "" || targetRef == branchRef {
			return false
		}
		targetSHA := commitSHA(ctx, git, targetRef, "")
		if targetSHA == "" {
			return false
		}
		return git(ctx, []string{"merge-base", "--is-ancestor", headSHA, targetSHA}, GitOptions{}).ExitCode == 0
	}

	remoteDefault := resolveRemoteDefaultRef(ctx, git)
	if remoteDefault != "" {
		if isAncestor(remoteDefault) {
			return true
		}
		if !repo.ObjectStoreInsideWorktree {
			if localDefault := localBranchForRemoteRef(remoteDefault); isAncestor(localDefault) {
				return true
			}
		}
	}

	upstream := git(ctx, []string{"rev-parse", "--symbolic-full-name", "--quiet", "@{upstream}"}, GitOptions{})
	if upstream.ExitCode == 0 {
		upstreamRef := strings.TrimSpace(upstream.Stdout)
		if upstreamRef != "" {
			branchShort := strings.TrimPrefix(branchRef, "refs/heads/")
			upstreamShort := shortBranchName(upstreamRef)
			if upstreamShort != branchShort {
				if isAncestor(upstreamRef) {
					return true
				}
			} else if isAncestor(upstreamRef) {
				if remoteDefault == "" {
					hasKnownRemoteDefault := false
					for _, candidate := range []string{"refs/remotes/origin/main", "refs/remotes/origin/master"} {
						if commitSHA(ctx, git, candidate, "") != "" {
							hasKnownRemoteDefault = true
							if isAncestor(candidate) {
								return true
							}
						}
					}
					if !hasKnownRemoteDefault {
						return true
					}
				}
			}
		}
	}

	if !repo.ObjectStoreInsideWorktree {
		if hostRef := readCommonHeadRef(repo.CommonDir); isAncestor(hostRef) {
			return true
		}
	}

	for _, candidate := range []string{"refs/remotes/origin/main", "refs/remotes/origin/master"} {
		if isAncestor(candidate) {
			return true
		}
	}
	if !repo.ObjectStoreInsideWorktree {
		for _, candidate := range []string{"refs/heads/main", "refs/heads/master"} {
			if isAncestor(candidate) {
				return true
			}
		}
	}
	return false
}

func resolveRemoteDefaultRef(ctx context.Context, git GitRun) string {
	origin := git(ctx, []string{"symbolic-ref", "-q", "refs/remotes/origin/HEAD"}, GitOptions{})
	if origin.ExitCode == 0 {
		if ref := strings.TrimSpace(origin.Stdout); ref != "" {
			return ref
		}
	}
	refs := git(ctx, []string{"for-each-ref", "--format=%(refname)", "refs/remotes/*/HEAD"}, GitOptions{Whole: true})
	if refs.ExitCode != 0 {
		return ""
	}
	for _, line := range strings.Split(refs.Stdout, "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		sym := git(ctx, []string{"symbolic-ref", "-q", name}, GitOptions{})
		if sym.ExitCode == 0 {
			if ref := strings.TrimSpace(sym.Stdout); ref != "" {
				return ref
			}
		}
	}
	return ""
}

func localBranchForRemoteRef(ref string) string {
	rest, ok := strings.CutPrefix(ref, "refs/remotes/")
	if !ok {
		return ""
	}
	_, name, ok := strings.Cut(rest, "/")
	if !ok || name == "" {
		return ""
	}
	return "refs/heads/" + name
}

func shortBranchName(ref string) string {
	if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return name
	}
	if rest, ok := strings.CutPrefix(ref, "refs/remotes/"); ok {
		if _, name, ok := strings.Cut(rest, "/"); ok {
			return name
		}
	}
	return ref
}

func readCommonHeadRef(commonDir string) string {
	if commonDir == "" {
		return ""
	}
	raw, err := readSmallTextFile(filepath.Join(commonDir, "HEAD"))
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(raw, "\n")
	line = strings.TrimRight(line, "\r")
	ref, ok := strings.CutPrefix(line, "ref:")
	if !ok {
		return ""
	}
	return strings.TrimSpace(ref)
}

func embeddedReposLanded(ctx context.Context, directory string) (bool, error) {
	repos, err := DiscoverEmbeddedRepos(ctx, directory)
	if err != nil {
		return false, err
	}
	for _, repo := range repos {
		status, err := InspectEmbeddedRepo(ctx, repo)
		if err != nil {
			return false, err
		}
		if status.Dirty || status.Unmerged || (repo.ObjectStoreInsideWorktree && status.Unpushed) {
			return false, nil
		}
	}
	return true, nil
}

func nestedWorktreesDeepestFirst(repos []EmbeddedRepo) []EmbeddedRepo {
	var nested []EmbeddedRepo
	for _, repo := range repos {
		if repo.Kind == EmbeddedWorktree && repo.RegisteredWorktreePath != "" {
			nested = append(nested, repo)
		}
	}
	slices.SortFunc(nested, func(a, b EmbeddedRepo) int {
		if c := cmp.Compare(b.Depth, a.Depth); c != 0 {
			return c
		}
		if c := cmp.Compare(len(b.Path), len(a.Path)); c != 0 {
			return c
		}
		return cmp.Compare(b.Path, a.Path)
	})
	return nested
}

func removeNestedWorktrees(ctx context.Context, repos []EmbeddedRepo, discard bool) (bool, error) {
	nested := nestedWorktreesDeepestFirst(repos)
	for _, repo := range nested {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		repoInfo, err := os.Lstat(repo.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			if discard {
				continue
			}
			return false, err
		}
		regInfo, err := os.Lstat(repo.RegisteredWorktreePath)
		if err != nil || !regInfo.IsDir() || !os.SameFile(repoInfo, regInfo) {
			if discard {
				continue
			}
			return false, nil
		}
		hostGit := NewGit(repo.CommonDir, "file")
		args := []string{"--git-dir", repo.CommonDir, "worktree", "remove"}
		if discard {
			args = append(args, "--force", "--force")
		}
		args = append(args, "--", repo.RegisteredWorktreePath)
		result := hostGit(ctx, args, GitOptions{Directory: repo.CommonDir})
		if result.ExitCode == 0 {
			continue
		}
		if discard {
			_ = hostGit(ctx, []string{"--git-dir", repo.CommonDir, "worktree", "prune"}, GitOptions{Directory: repo.CommonDir})
			continue
		}
		lower := strings.ToLower(result.Stderr)
		if strings.Contains(lower, "modified or untracked") || strings.Contains(lower, "use --force") || strings.Contains(lower, "locked") {
			return false, nil
		}
		return false, gitFailure(result, "git worktree remove")
	}
	return true, nil
}
