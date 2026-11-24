package local

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/daidr/Ariadne/sdk"
)

const EmptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

const maxEmbeddedWorkers = 4

type embeddedStatus struct {
	headSHA     string
	branch      string
	dirty       bool
	uncommitted []string
}

// parsePorcelainV2Status parses `git status --porcelain=v2 --branch -z --untracked-files=normal`.
func parsePorcelainV2Status(output string) embeddedStatus {
	st := embeddedStatus{uncommitted: []string{}}
	tokens := strings.Split(output, "\x00")
	for i := 0; i < len(tokens); i++ {
		rec := tokens[i]
		if rec == "" {
			continue
		}
		if strings.HasPrefix(rec, "# ") {
			if oid, ok := strings.CutPrefix(rec, "# branch.oid "); ok {
				oid = strings.TrimSpace(oid)
				if oid != "" && oid != "(initial)" {
					st.headSHA = oid
				}
			} else if head, ok := strings.CutPrefix(rec, "# branch.head "); ok {
				head = strings.TrimSpace(head)
				if head == "(detached)" {
					st.branch = "HEAD"
				} else if head != "" {
					st.branch = head
				}
			}
			continue
		}
		switch rec[0] {
		case '1':
			st.dirty = true
			if parts := strings.SplitN(rec, " ", 9); len(parts) == 9 && parts[8] != "" {
				st.uncommitted = append(st.uncommitted, parts[8])
			}
		case '2':
			st.dirty = true
			if parts := strings.SplitN(rec, " ", 10); len(parts) == 10 && parts[9] != "" {
				st.uncommitted = append(st.uncommitted, parts[9])
			}
			// Rename/copy entries carry the original path in the next NUL token.
			i++
		case 'u':
			st.dirty = true
			if parts := strings.SplitN(rec, " ", 11); len(parts) == 11 && parts[10] != "" {
				st.uncommitted = append(st.uncommitted, parts[10])
			}
		case '?':
			st.dirty = true
			if len(rec) > 2 {
				st.uncommitted = append(st.uncommitted, rec[2:])
			}
		}
	}
	return st
}

func readEmbeddedStatus(ctx context.Context, git GitRun, repoDir string) (embeddedStatus, bool, error) {
	args := []string{"--no-optional-locks"}
	args = append(args, worktreeConfigArgs(ctx, repoDir)...)
	args = append(args, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=normal")
	res := git(ctx, args, GitOptions{Directory: repoDir, Whole: true})
	if err := ctx.Err(); err != nil {
		return embeddedStatus{}, false, err
	}
	if res.ExitCode != 0 {
		return embeddedStatus{}, false, nil
	}
	return parsePorcelainV2Status(res.Stdout), true, nil
}

// resolveEmbeddedRepoPath validates that repoPath is a safe relative path
// inside worktreeDir pointing (without symlinks) to a valid embedded .git repo.
func resolveEmbeddedRepoPath(ctx context.Context, worktreeDir, repoPath string) (EmbeddedRepo, error) {
	clean, err := TreePath(repoPath)
	if err != nil {
		return EmbeddedRepo{}, fmt.Errorf("invalid embedded repository path: %w", err)
	}
	root, err := canonicalOrClean(worktreeDir)
	if err != nil {
		return EmbeddedRepo{}, err
	}
	parts := strings.Split(clean, "/")
	current := root
	parentRepoDir := ""
	if dirHasValidGitEntry(root) {
		parentRepoDir = root
	}
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return EmbeddedRepo{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return EmbeddedRepo{}, errors.New("embedded repository path is not a directory")
		}
		if i < len(parts)-1 && dirHasValidGitEntry(current) {
			parentRepoDir = current
		}
	}
	if !isPathWithin(current, root) {
		return EmbeddedRepo{}, errors.New("embedded repository path escapes worktree")
	}
	gitPath := filepath.Join(current, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return EmbeddedRepo{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return EmbeddedRepo{}, errors.New("embedded repository .git must not be a symlink")
	}
	var repo EmbeddedRepo
	if info.IsDir() {
		repo = EmbeddedRepo{
			Path:                      current,
			RelPath:                   clean,
			Depth:                     len(parts),
			Kind:                      EmbeddedStandalone,
			GitDir:                    gitPath,
			CommonDir:                 gitPath,
			ObjectStoreInsideWorktree: true,
		}
	} else if info.Mode().IsRegular() {
		repo = inspectEmbeddedGitFile(ctx, root, current, clean, len(parts), gitPath)
	} else {
		return EmbeddedRepo{}, errors.New("invalid embedded repository .git entry")
	}
	if repo.GitDir == "" {
		return EmbeddedRepo{}, errors.New("invalid embedded repository metadata")
	}
	repoGit := NewGit(repo.Path, "file")
	ok, err := verifyEmbeddedGitDir(ctx, repoGit, repo.GitDir)
	if err != nil {
		return EmbeddedRepo{}, err
	}
	if !ok {
		return EmbeddedRepo{}, errors.New("embedded repository gitdir verification failed")
	}
	if err := populateSubmoduleMetadata(ctx, root, parentRepoDir, &repo); err != nil {
		return EmbeddedRepo{}, err
	}
	return repo, nil
}

func displayRefName(ref string) string {
	if rest, ok := strings.CutPrefix(ref, "refs/remotes/"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return rest
	}
	return ref
}

func shortCommitSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func mergeBaseSHA(ctx context.Context, git GitRun, a, b string) string {
	if a == "" || b == "" {
		return ""
	}
	res := git(ctx, []string{"merge-base", a, b}, GitOptions{})
	if res.ExitCode != 0 {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

func isAncestorSHA(ctx context.Context, git GitRun, ancestor, descendant string) bool {
	if ancestor == "" || descendant == "" {
		return false
	}
	return git(ctx, []string{"merge-base", "--is-ancestor", ancestor, descendant}, GitOptions{}).ExitCode == 0
}

// resolveEmbeddedFallbackBase finds the default remote/upstream base ref for an
// embedded repository when no recorded first-touch baseline is available (or to
// upgrade a baseline that tracks an advancing default branch). Only candidates
// that share history (a valid merge-base) with headSHA are returned.
func resolveEmbeddedFallbackBase(ctx context.Context, git GitRun, repo EmbeddedRepo, branchRef, headSHA string) (ref, name, sha, mergeBase string) {
	upstreamRef := resolveUpstreamRef(ctx, git, "")
	upstreamRemote, upstreamShort, hasRemoteUpstream := remoteNameAndBranch(upstreamRef)
	branchShort := strings.TrimPrefix(branchRef, "refs/heads/")

	checked := make(map[string]bool)
	tryCandidate := func(candidate string) (string, string, string, string, bool) {
		if candidate == "" || candidate == branchRef || checked[candidate] {
			return "", "", "", "", false
		}
		checked[candidate] = true
		resolved := commitSHA(ctx, git, candidate, "")
		if resolved == "" {
			return "", "", "", "", false
		}
		mb := resolved
		if headSHA != "" && resolved != headSHA {
			mb = mergeBaseSHA(ctx, git, resolved, headSHA)
			if mb == "" {
				return "", "", "", "", false
			}
		}
		return candidate, displayRefName(candidate), resolved, mb, true
	}

	// 1. Upstream tracking a differently named branch (e.g. feat/work -> origin/develop).
	if upstreamRef != "" && branchRef != "" {
		uShort := upstreamShort
		if !hasRemoteUpstream {
			uShort = shortBranchName(upstreamRef)
		}
		if uShort != "" && uShort != branchShort {
			if r, n, s, mb, ok := tryCandidate(upstreamRef); ok {
				return r, n, s, mb
			}
		}
	}

	// 2. For Git submodules, prefer the branch declared in the parent's .gitmodules.
	if repo.IsSubmodule() {
		if configuredBranch := readSubmoduleConfiguredBranch(ctx, repo.SuperprojectPath, repo.SubmodulePath); configuredBranch != "" {
			if hasRemoteUpstream {
				if r, n, s, mb, ok := tryCandidate("refs/remotes/" + upstreamRemote + "/" + configuredBranch); ok {
					return r, n, s, mb
				}
			}
			if r, n, s, mb, ok := tryCandidate("refs/remotes/origin/" + configuredBranch); ok {
				return r, n, s, mb
			}
			if onlyRemote := soleRemoteName(ctx, git); onlyRemote != "" {
				if r, n, s, mb, ok := tryCandidate("refs/remotes/" + onlyRemote + "/" + configuredBranch); ok {
					return r, n, s, mb
				}
			}
			if r, n, s, mb, ok := tryCandidate("refs/heads/" + configuredBranch); ok {
				return r, n, s, mb
			}
		}
	}

	// 3. Explicit remote default branch (refs/remotes/<remote>/HEAD).
	if remoteDefault := resolveRemoteDefaultRef(ctx, git, upstreamRemote); remoteDefault != "" {
		if r, n, s, mb, ok := tryCandidate(remoteDefault); ok {
			return r, n, s, mb
		}
		if !repo.ObjectStoreInsideWorktree && !repo.IsSubmodule() {
			if r, n, s, mb, ok := tryCandidate(localBranchForRemoteRef(remoteDefault)); ok {
				return r, n, s, mb
			}
		}
	}

	// 4. Host repository HEAD for external object stores (nested linked worktrees).
	if !repo.ObjectStoreInsideWorktree && !repo.IsSubmodule() {
		if hostRef := readCommonHeadRef(repo.CommonDir); hostRef != "" {
			if hostUpstream := resolveUpstreamRef(ctx, git, hostRef); hostUpstream != "" {
				if r, n, s, mb, ok := tryCandidate(hostUpstream); ok {
					return r, n, s, mb
				}
			}
			if hostShort, ok := strings.CutPrefix(hostRef, "refs/heads/"); ok && hostShort != "" {
				if hasRemoteUpstream {
					if r, n, s, mb, ok := tryCandidate("refs/remotes/" + upstreamRemote + "/" + hostShort); ok {
						return r, n, s, mb
					}
				}
				if r, n, s, mb, ok := tryCandidate("refs/remotes/origin/" + hostShort); ok {
					return r, n, s, mb
				}
			}
			if r, n, s, mb, ok := tryCandidate(hostRef); ok {
				return r, n, s, mb
			}
		}
	}

	// 5. Conventional default branch names (main / master).
	if hasRemoteUpstream {
		for _, candidate := range []string{"refs/remotes/" + upstreamRemote + "/main", "refs/remotes/" + upstreamRemote + "/master"} {
			if r, n, s, mb, ok := tryCandidate(candidate); ok {
				return r, n, s, mb
			}
		}
	}
	for _, candidate := range []string{"refs/remotes/origin/main", "refs/remotes/origin/master"} {
		if r, n, s, mb, ok := tryCandidate(candidate); ok {
			return r, n, s, mb
		}
	}
	if onlyRemote := soleRemoteName(ctx, git); onlyRemote != "" {
		for _, candidate := range []string{"refs/remotes/" + onlyRemote + "/main", "refs/remotes/" + onlyRemote + "/master"} {
			if r, n, s, mb, ok := tryCandidate(candidate); ok {
				return r, n, s, mb
			}
		}
	}
	for _, candidate := range []string{"refs/heads/main", "refs/heads/master"} {
		if r, n, s, mb, ok := tryCandidate(candidate); ok {
			return r, n, s, mb
		}
	}

	// 6. Same-named upstream tracking ref (shows local unpushed commits).
	if upstreamRef != "" {
		if r, n, s, mb, ok := tryCandidate(upstreamRef); ok {
			return r, n, s, mb
		}
	}

	return "", "", "", ""
}

// EmbeddedRepoChanges computes the branch diff and uncommitted status for one
// embedded repository. Returns (nil, nil) when the repository is invalid or has
// no commits, changed files, or uncommitted modifications relative to its base.
func EmbeddedRepoChanges(ctx context.Context, repo EmbeddedRepo, baseline *sdk.RepoBaseline) (*sdk.BranchChanges, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if repo.GitDir == "" {
		return nil, nil
	}
	repoGit := NewGit(repo.Path, "file")
	valid, err := verifyEmbeddedGitDir(ctx, repoGit, repo.GitDir)
	if err != nil || !valid {
		return nil, err
	}
	status, ok, err := readEmbeddedStatus(ctx, repoGit, repo.Path)
	if err != nil || !ok {
		return nil, err
	}
	branchName := status.branch
	if branchName == "" {
		branchName = "HEAD"
	}
	if status.headSHA == "" {
		if len(status.uncommitted) == 0 {
			return nil, nil
		}
		return &sdk.BranchChanges{
			Branch:      branchName,
			Base:        "",
			From:        "",
			Head:        "",
			Commits:     []sdk.BranchCommit{},
			Files:       []sdk.ChangedFile{},
			Uncommitted: status.uncommitted,
		}, nil
	}

	branchRef := ""
	if branchName != "HEAD" {
		branchRef = "refs/heads/" + branchName
	}
	var from, baseSHA, baseName string
	recordedSHA := ""
	if baseline != nil && baseline.BaseHead != "" {
		recordedSHA = commitSHA(ctx, repoGit, baseline.BaseHead, "")
	}
	if recordedSHA != "" {
		_, defaultName, defaultSHA, defaultMB := resolveEmbeddedFallbackBase(ctx, repoGit, repo, branchRef, status.headSHA)
		if defaultSHA != "" && defaultMB != "" && defaultSHA != recordedSHA &&
			isAncestorSHA(ctx, repoGit, recordedSHA, defaultSHA) &&
			isAncestorSHA(ctx, repoGit, recordedSHA, defaultMB) {
			baseSHA = defaultSHA
			from = defaultMB
			baseName = defaultName
		}
		if from == "" {
			baseSHA = recordedSHA
			mb := mergeBaseSHA(ctx, repoGit, baseSHA, status.headSHA)
			if mb != "" {
				from = mb
				switch {
				case defaultSHA == baseSHA && defaultName != "":
					baseName = defaultName
				case baseline.BaseBranch != "" && baseline.BaseBranch != branchName:
					baseName = baseline.BaseBranch
				default:
					baseName = shortCommitSHA(baseSHA)
				}
			} else if defaultSHA != "" && defaultMB != "" {
				// recordedSHA has no common ancestor with HEAD (for example after switching
				// to an unrelated orphan branch): fall back to a connected default base
				// rather than diffing two unrelated trees.
				baseSHA = defaultSHA
				from = defaultMB
				baseName = defaultName
			} else {
				baseSHA = status.headSHA
				from = status.headSHA
				baseName = branchName
			}
		}
	} else if repo.IsSubmodule() {
		gitlinkSHA := readSubmoduleGitlink(ctx, repo.SuperprojectPath, repo.SubmodulePath)
		switch {
		case gitlinkSHA != "" && gitlinkSHA == status.headSHA:
			baseSHA = gitlinkSHA
			from = status.headSHA
			baseName = shortCommitSHA(gitlinkSHA)
		case gitlinkSHA != "":
			if mb := mergeBaseSHA(ctx, repoGit, gitlinkSHA, status.headSHA); mb != "" {
				baseSHA = gitlinkSHA
				from = mb
				baseName = shortCommitSHA(gitlinkSHA)
			} else if _, defaultName, defaultSHA, defaultMB := resolveEmbeddedFallbackBase(ctx, repoGit, repo, branchRef, status.headSHA); defaultSHA != "" && defaultMB != "" {
				baseSHA = defaultSHA
				from = defaultMB
				baseName = defaultName
			} else {
				baseSHA = status.headSHA
				from = status.headSHA
				baseName = branchName
			}
		default:
			if _, defaultName, defaultSHA, defaultMB := resolveEmbeddedFallbackBase(ctx, repoGit, repo, branchRef, status.headSHA); defaultSHA != "" && defaultMB != "" {
				baseSHA = defaultSHA
				from = defaultMB
				baseName = defaultName
			} else {
				baseSHA = status.headSHA
				from = status.headSHA
				baseName = branchName
			}
		}
	} else if _, defaultName, defaultSHA, defaultMB := resolveEmbeddedFallbackBase(ctx, repoGit, repo, branchRef, status.headSHA); defaultSHA != "" && defaultMB != "" {
		baseSHA = defaultSHA
		baseName = defaultName
		from = defaultMB
	} else {
		baseSHA = status.headSHA
		from = status.headSHA
		baseName = branchName
	}

	files := []sdk.ChangedFile{}
	commits := []sdk.BranchCommit{}
	if from != "" && status.headSHA != "" && from != status.headSHA {
		var (
			diffErr   error
			logResult sdk.ExecResult
			wg        sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			files, diffErr = TreeDiff(ctx, repoGit, from, status.headSHA)
		}()
		go func() {
			defer wg.Done()
			logResult = repoGit(ctx, []string{"log", "-z", "--max-count=100", "--format=%H %s", from + ".." + status.headSHA}, GitOptions{Whole: true})
		}()
		wg.Wait()
		if diffErr != nil {
			return nil, diffErr
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
	}

	if len(files) == 0 && len(commits) == 0 && len(status.uncommitted) == 0 {
		return nil, nil
	}
	return &sdk.BranchChanges{
		Branch:      branchName,
		Base:        baseName,
		From:        from,
		Head:        status.headSHA,
		BaseHead:    baseSHA,
		Commits:     commits,
		Files:       files,
		Uncommitted: status.uncommitted,
	}, nil
}

func snapshotEmbeddedRepo(ctx context.Context, repo EmbeddedRepo, threadID string) (sdk.RepoSnapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return sdk.RepoSnapshot{}, false, err
	}
	if repo.GitDir == "" {
		return sdk.RepoSnapshot{}, false, nil
	}
	repoGit := NewGit(repo.Path, "file")
	valid, err := verifyEmbeddedGitDir(ctx, repoGit, repo.GitDir)
	if err != nil || !valid {
		return sdk.RepoSnapshot{}, false, err
	}
	status, ok, err := readEmbeddedStatus(ctx, repoGit, repo.Path)
	if err != nil || !ok {
		return sdk.RepoSnapshot{}, false, err
	}
	branchName := status.branch
	if branchName == "" {
		branchName = "HEAD"
	}
	if !status.dirty {
		if status.headSHA == "" {
			return sdk.RepoSnapshot{}, false, nil
		}
		return sdk.RepoSnapshot{
			RepoPath: repo.RelPath,
			Branch:   branchName,
			Head:     status.headSHA,
			Tree:     status.headSHA,
		}, true, nil
	}
	tree, err := WorktreeSnapshot(ctx, repoGit, repo.Path, threadID)
	if err != nil {
		return sdk.RepoSnapshot{}, false, err
	}
	return sdk.RepoSnapshot{
		RepoPath: repo.RelPath,
		Branch:   branchName,
		Head:     status.headSHA,
		Tree:     tree,
	}, true, nil
}

func pinSnapshotObject(ctx context.Context, git GitRun, cwd, threadID, sha string) {
	if !isObjectID(sha) || sha == EmptyTreeSHA {
		return
	}
	ref := SnapshotRefs(threadID) + sha
	if validRef(ctx, git, ref) != nil {
		return
	}
	_ = git(ctx, []string{"update-ref", ref, sha}, GitOptions{Directory: cwd})
}

func collectExternalEmbeddedCommonDirs(ctx context.Context, worktreeDir string) []string {
	repos, err := DiscoverEmbeddedRepos(ctx, worktreeDir)
	if err != nil || len(repos) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(repos))
	var commonDirs []string
	for _, repo := range repos {
		if repo.ObjectStoreInsideWorktree || repo.GitDir == "" || repo.CommonDir == "" {
			continue
		}
		repoGit := NewGit(repo.Path, "file")
		if valid, err := verifyEmbeddedGitDir(ctx, repoGit, repo.GitDir); err != nil || !valid {
			continue
		}
		key := cleanPathKey(repo.CommonDir)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		commonDirs = append(commonDirs, repo.CommonDir)
	}
	return commonDirs
}

func dropSnapshotsInCommonDirs(ctx context.Context, commonDirs []string, threadID string) {
	for _, commonDir := range commonDirs {
		if commonDir == "" {
			continue
		}
		info, err := os.Stat(commonDir)
		if err != nil || !info.IsDir() {
			continue
		}
		baseGit := NewGit(commonDir, "file")
		repoGit := func(ctx context.Context, args []string, options GitOptions) sdk.ExecResult {
			env := map[string]string{"GIT_DIR": commonDir}
			maps.Copy(env, options.Env)
			options.Env = env
			return baseGit(ctx, args, options)
		}
		_ = DropSnapshots(ctx, repoGit, threadID)
	}
}

func (factory *Workspaces) EmbeddedChanges(ctx context.Context, threadID string, baselines map[string]sdk.RepoBaseline) ([]sdk.RepoChanges, error) {
	ctx, done, err := factory.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	dir, err := factory.checkedDirectory(threadID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	repos, err := DiscoverEmbeddedRepos(ctx, dir)
	if err != nil || len(repos) == 0 {
		return nil, err
	}
	results := make([]*sdk.RepoChanges, len(repos))
	workers := min(maxEmbeddedWorkers, len(repos))
	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	for worker := range workers {
		wg.Go(func() {
			for i := worker; i < len(repos) && ctx.Err() == nil; i += workers {
				repo := repos[i]
				var basePtr *sdk.RepoBaseline
				if baselines != nil {
					if b, ok := baselines[repo.RelPath]; ok {
						bCopy := b
						basePtr = &bCopy
					}
				}
				changes, err := EmbeddedRepoChanges(ctx, repo, basePtr)
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					continue
				}
				if changes != nil {
					results[i] = &sdk.RepoChanges{
						RepoPath: repo.RelPath,
						Embedded: true,
						Changes:  changes,
					}
				}
			}
		})
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]sdk.RepoChanges, 0, len(results))
	for _, item := range results {
		if item != nil {
			out = append(out, *item)
		}
	}
	slices.SortFunc(out, func(a, b sdk.RepoChanges) int {
		return cmp.Compare(a.RepoPath, b.RepoPath)
	})
	return out, nil
}

func (factory *Workspaces) RepoSnapshots(ctx context.Context, threadID string) ([]sdk.RepoSnapshot, error) {
	ctx, done, err := factory.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	release, err := factory.thread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	defer release()
	dir, err := factory.checkedDirectory(threadID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := factory.verifiedWorktree(ctx, dir); err != nil {
		return nil, err
	}
	rootTree, err := WorktreeSnapshot(ctx, factory.git, dir, threadID)
	if err != nil {
		return nil, err
	}
	out := []sdk.RepoSnapshot{{RepoPath: "", Tree: rootTree}}

	repos, err := DiscoverEmbeddedRepos(ctx, dir)
	if err != nil || len(repos) == 0 {
		return out, err
	}
	captured := make([]*sdk.RepoSnapshot, len(repos))
	workers := min(maxEmbeddedWorkers, len(repos))
	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)
	for worker := range workers {
		wg.Go(func() {
			for i := worker; i < len(repos) && ctx.Err() == nil; i += workers {
				snap, ok, err := snapshotEmbeddedRepo(ctx, repos[i], threadID)
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					errMu.Unlock()
					continue
				}
				if ok {
					sCopy := snap
					captured[i] = &sCopy
				}
			}
		})
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, snap := range captured {
		if snap != nil {
			out = append(out, *snap)
		}
	}
	return out, nil
}

func (factory *Workspaces) RepoDiff(ctx context.Context, threadID, repoPath, from, to string) ([]sdk.ChangedFile, error) {
	ctx, done, err := factory.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if repoPath == "" {
		return TreeDiff(ctx, factory.git, from, to)
	}
	dir, err := factory.checkedDirectory(threadID)
	if err != nil {
		return nil, err
	}
	repo, err := resolveEmbeddedRepoPath(ctx, dir, repoPath)
	if err != nil {
		return nil, err
	}
	repoGit := NewGit(repo.Path, "file")
	files, err := TreeDiff(ctx, repoGit, from, to)
	if err != nil {
		return nil, err
	}
	if len(files) > 0 && threadID != "" {
		pinSnapshotObject(ctx, repoGit, repo.Path, threadID, from)
		pinSnapshotObject(ctx, repoGit, repo.Path, threadID, to)
	}
	return files, nil
}
