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

func verifyEmbeddedGitDir(ctx context.Context, repoGit GitRun, expectedGitDir string) (bool, error) {
	expectedInfo, err := os.Stat(expectedGitDir)
	if err != nil || !expectedInfo.IsDir() {
		return false, nil
	}
	res := repoGit(ctx, []string{"rev-parse", "--path-format=absolute", "--git-dir"}, GitOptions{})
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		return false, nil
	}
	actualPath := filepath.FromSlash(strings.TrimSpace(res.Stdout))
	if actualPath == "" {
		return false, nil
	}
	actualInfo, err := os.Stat(actualPath)
	if err != nil || !actualInfo.IsDir() {
		return false, nil
	}
	return os.SameFile(expectedInfo, actualInfo), nil
}

// InspectEmbeddedRepo evaluates whether an embedded repository has uncommitted
// work, in-progress Git operations, unpushed commits in an in-worktree object
// store, or commits not yet merged into its base/default branch.
func InspectEmbeddedRepo(ctx context.Context, repo EmbeddedRepo) (EmbeddedRepoStatus, error) {
	if err := ctx.Err(); err != nil {
		return EmbeddedRepoStatus{}, err
	}
	status := EmbeddedRepoStatus{Repo: repo}
	failClosed := func() EmbeddedRepoStatus {
		status.Dirty = true
		status.Unmerged = true
		status.Unpushed = repo.ObjectStoreInsideWorktree
		return status
	}
	if repo.GitDir == "" || (repo.Kind == EmbeddedWorktree && repo.RegisteredWorktreePath == "") {
		return failClosed(), nil
	}
	repoGit := NewGit(repo.Path, "file")
	validGitDir, err := verifyEmbeddedGitDir(ctx, repoGit, repo.GitDir)
	if err != nil {
		return EmbeddedRepoStatus{}, err
	}
	if !validGitDir {
		return failClosed(), nil
	}
	for _, marker := range inProgressGitMarkers {
		if _, err := os.Lstat(filepath.Join(repo.GitDir, marker)); err == nil || !os.IsNotExist(err) {
			status.Dirty = true
			break
		}
	}
	if repo.Kind == EmbeddedWorktree {
		if _, err := os.Lstat(filepath.Join(repo.GitDir, "locked")); err == nil || !os.IsNotExist(err) {
			status.Dirty = true
		}
	}

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

	upstreamRef := resolveUpstreamRef(ctx, git, "")
	upstreamRemote, upstreamShort, hasRemoteUpstream := remoteNameAndBranch(upstreamRef)

	// 1. Explicit remote default branch (refs/remotes/<remote>/HEAD), preferring
	//    the current branch's remote, then origin, then any remote HEAD.
	remoteDefault := resolveRemoteDefaultRef(ctx, git, upstreamRemote)
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

	// 2. For external object stores (linked worktrees / submodules sharing host
	//    git dir), check the host repo's HEAD branch and its remote counterpart.
	hostRef := ""
	hostHasCommit := false
	if !repo.ObjectStoreInsideWorktree {
		hostRef = readCommonHeadRef(repo.CommonDir)
		if hostRef != "" {
			if commitSHA(ctx, git, hostRef, "") != "" {
				hostHasCommit = true
			}
			if isAncestor(hostRef) {
				return true
			}
			if hostUpstream := resolveUpstreamRef(ctx, git, hostRef); isAncestor(hostUpstream) {
				return true
			}
			if hostShort, ok := strings.CutPrefix(hostRef, "refs/heads/"); ok && hostShort != "" {
				if hasRemoteUpstream && isAncestor("refs/remotes/"+upstreamRemote+"/"+hostShort) {
					return true
				}
				if isAncestor("refs/remotes/origin/" + hostShort) {
					return true
				}
			}
		}
	}

	// 3. Fallback default branch names (main / master) on the current branch's
	//    remote, origin, a sole configured remote, and (for external object
	//    stores) local branches.
	hasExplicitDefaultCandidate := remoteDefault != "" || (hostHasCommit && hostRef != branchRef)
	checkedCandidates := make(map[string]bool)
	checkCandidate := func(candidate string) bool {
		if candidate == "" || checkedCandidates[candidate] {
			return false
		}
		checkedCandidates[candidate] = true
		if commitSHA(ctx, git, candidate, "") == "" {
			return false
		}
		hasExplicitDefaultCandidate = true
		return isAncestor(candidate)
	}

	if hasRemoteUpstream {
		if checkCandidate("refs/remotes/"+upstreamRemote+"/main") ||
			checkCandidate("refs/remotes/"+upstreamRemote+"/master") {
			return true
		}
	}
	if checkCandidate("refs/remotes/origin/main") || checkCandidate("refs/remotes/origin/master") {
		return true
	}
	if !hasExplicitDefaultCandidate {
		if onlyRemote := soleRemoteName(ctx, git); onlyRemote != "" {
			if checkCandidate("refs/remotes/"+onlyRemote+"/main") ||
				checkCandidate("refs/remotes/"+onlyRemote+"/master") {
				return true
			}
		}
	}
	if !repo.ObjectStoreInsideWorktree {
		if checkCandidate("refs/heads/main") || checkCandidate("refs/heads/master") {
			return true
		}
	}

	// 4. Current branch's own upstream tracking ref:
	//    - If tracking a differently named branch (e.g. feat/work -> origin/develop),
	//      being an ancestor of that upstream means no unmerged commits over its base.
	//    - If tracking a same-named remote branch (e.g. feat/work -> origin/feat/work),
	//      only treat it as merged when no default branch or host HEAD conflicts
	//      and this is a pure single-branch clone with no other branches.
	if upstreamRef != "" {
		branchShort := strings.TrimPrefix(branchRef, "refs/heads/")
		if !hasRemoteUpstream {
			upstreamShort = shortBranchName(upstreamRef)
		}
		if upstreamShort != branchShort {
			if isAncestor(upstreamRef) {
				return true
			}
		} else if hasRemoteUpstream && !hasExplicitDefaultCandidate && isAncestor(upstreamRef) {
			if isPureSingleBranchTracking(ctx, git, repo, branchRef, upstreamRef, upstreamRemote, hostRef, hostHasCommit) {
				return true
			}
		}
	}

	return false
}

func resolveUpstreamRef(ctx context.Context, git GitRun, branchRef string) string {
	if branchRef == "" {
		res := git(ctx, []string{"rev-parse", "--symbolic-full-name", "--quiet", "@{upstream}"}, GitOptions{})
		if res.ExitCode != 0 {
			return ""
		}
		return strings.TrimSpace(res.Stdout)
	}
	res := git(ctx, []string{"for-each-ref", "--format=%(refname)\t%(upstream)", branchRef}, GitOptions{Whole: true})
	if res.ExitCode != 0 {
		return ""
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		name, upstream, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if ok && name == branchRef {
			return strings.TrimSpace(upstream)
		}
	}
	return ""
}

func remoteNameAndBranch(ref string) (remote, branch string, ok bool) {
	rest, ok := strings.CutPrefix(ref, "refs/remotes/")
	if !ok {
		return "", "", false
	}
	remote, branch, ok = strings.Cut(rest, "/")
	if !ok || remote == "" || branch == "" {
		return "", "", false
	}
	return remote, branch, true
}

func soleRemoteName(ctx context.Context, git GitRun) string {
	refs := git(ctx, []string{"for-each-ref", "--format=%(refname)", "refs/remotes"}, GitOptions{Whole: true})
	if refs.ExitCode != 0 {
		return ""
	}
	sole := ""
	for _, line := range strings.Split(refs.Stdout, "\n") {
		remote, _, ok := remoteNameAndBranch(strings.TrimSpace(line))
		if !ok {
			continue
		}
		if sole == "" {
			sole = remote
		} else if sole != remote {
			return ""
		}
	}
	return sole
}

func isPureSingleBranchTracking(ctx context.Context, git GitRun, repo EmbeddedRepo, branchRef, upstreamRef, upstreamRemote, hostRef string, hostHasCommit bool) bool {
	if !repo.ObjectStoreInsideWorktree {
		if hostHasCommit && hostRef != "" && hostRef != branchRef {
			return false
		}
	} else {
		localRefs := git(ctx, []string{"for-each-ref", "--format=%(refname)", "refs/heads"}, GitOptions{Whole: true})
		if localRefs.ExitCode != 0 {
			return false
		}
		for _, line := range strings.Split(localRefs.Stdout, "\n") {
			name := strings.TrimSpace(line)
			if name == "" {
				continue
			}
			if name != branchRef {
				return false
			}
		}
	}
	remoteRefs := git(ctx, []string{"for-each-ref", "--format=%(refname)", "refs/remotes"}, GitOptions{Whole: true})
	if remoteRefs.ExitCode != 0 {
		return false
	}
	branchShort := strings.TrimPrefix(branchRef, "refs/heads/")
	upstreamPrefix := "refs/remotes/" + upstreamRemote + "/"
	for _, line := range strings.Split(remoteRefs.Stdout, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasSuffix(name, "/HEAD") {
			continue
		}
		if strings.HasPrefix(name, upstreamPrefix) {
			if name != upstreamRef {
				return false
			}
			continue
		}
		if shortBranchName(name) != branchShort {
			return false
		}
	}
	return true
}

func resolveRemoteDefaultRef(ctx context.Context, git GitRun, preferredRemote string) string {
	tryHead := func(headRef string) string {
		sym := git(ctx, []string{"symbolic-ref", "-q", headRef}, GitOptions{})
		if sym.ExitCode == 0 {
			if ref := strings.TrimSpace(sym.Stdout); ref != "" {
				return ref
			}
		}
		return ""
	}
	if preferredRemote != "" {
		if ref := tryHead("refs/remotes/" + preferredRemote + "/HEAD"); ref != "" {
			return ref
		}
	}
	if preferredRemote != "origin" {
		if ref := tryHead("refs/remotes/origin/HEAD"); ref != "" {
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
		if ref := tryHead(name); ref != "" {
			return ref
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
