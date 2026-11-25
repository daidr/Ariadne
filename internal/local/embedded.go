package local

import (
	"cmp"
	"context"
	"errors"
	"io"
	"os"
	"path"
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
	maxGitmodulesBytes  = 1 << 20
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
	SuperprojectPath          string
	SubmodulePath             string
}

func (r EmbeddedRepo) IsSubmodule() bool {
	return r.SuperprojectPath != "" && r.SubmodulePath != ""
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

	ephemeralRoots := []string{root}
	if outerGitDir := linkedWorktreeGitDir(root); outerGitDir != "" {
		ephemeralRoots = append(ephemeralRoots, outerGitDir)
	}

	initialParentRepoDir := ""
	if dirHasValidGitEntry(root) {
		initialParentRepoDir = root
	}

	var repos []EmbeddedRepo
	var walk func(dir string, depth int, parentRepoDir string) error
	walk = func(dir string, depth int, parentRepoDir string) error {
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
		nextParentRepoDir := parentRepoDir
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
				var repo EmbeddedRepo
				recognized := false
				if entry.IsDir() {
					repo = EmbeddedRepo{
						Path:                      dir,
						RelPath:                   filepath.ToSlash(rel),
						Depth:                     depth,
						Kind:                      EmbeddedStandalone,
						GitDir:                    gitPath,
						CommonDir:                 gitPath,
						ObjectStoreInsideWorktree: true,
					}
					recognized = true
				} else if entryType.IsRegular() {
					repo = inspectEmbeddedGitFile(ctx, root, dir, filepath.ToSlash(rel), depth, gitPath)
					recognized = true
				}
				if recognized {
					if repo.GitDir != "" {
						if err := populateSubmoduleMetadata(ctx, root, parentRepoDir, &repo); err != nil {
							return err
						}
						nextParentRepoDir = dir
					}
					repos = append(repos, repo)
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
			if err := walk(filepath.Join(dir, name), depth+1, nextParentRepoDir); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(root, 0, initialParentRepoDir); err != nil {
		return nil, err
	}
	for _, repo := range repos {
		if repo.Kind == EmbeddedWorktree && repo.RegisteredWorktreePath != "" && repo.GitDir != "" {
			ephemeralRoots = append(ephemeralRoots, repo.GitDir)
		}
	}
	for {
		changed := false
		for i := range repos {
			if repos[i].ObjectStoreInsideWorktree || repos[i].CommonDir == "" {
				continue
			}
			if isPathWithinAny(repos[i].CommonDir, ephemeralRoots) {
				repos[i].ObjectStoreInsideWorktree = true
				if repos[i].GitDir != "" {
					ephemeralRoots = append(ephemeralRoots, repos[i].GitDir)
				}
				changed = true
			}
		}
		if !changed {
			break
		}
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

func resolveGitFileTarget(repoDir, gitFile string) (string, bool) {
	raw, err := readSmallTextFile(gitFile)
	if err != nil {
		return "", false
	}
	line, _, _ := strings.Cut(raw, "\n")
	line = strings.TrimRight(line, "\r")
	target, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", false
	}
	target = filepath.FromSlash(strings.TrimSpace(target))
	if target == "" {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(repoDir, target)
	}
	gitDir, err := canonicalOrClean(target)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return gitDir, true
}

func resolveRegularGitFileDir(dir string) string {
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ""
	}
	gitDir, ok := resolveGitFileTarget(dir, gitPath)
	if !ok {
		return ""
	}
	return gitDir
}

// linkedWorktreeGitDir returns dir's per-worktree gitdir (<common>/worktrees/<name>)
// when dir is a linked Git worktree whose admin gitdir points back to dir/.git.
func linkedWorktreeGitDir(dir string) string {
	gitPath := filepath.Join(dir, ".git")
	gitInfo, err := os.Lstat(gitPath)
	if err != nil || gitInfo.Mode()&os.ModeSymlink != 0 || !gitInfo.Mode().IsRegular() {
		return ""
	}
	gitDir, ok := resolveGitFileTarget(dir, gitPath)
	if !ok {
		return ""
	}
	if _, ok := linkedWorktreeCommonDir(gitDir); !ok {
		return ""
	}
	rawBack, err := readSmallTextFile(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return ""
	}
	backLine, _, _ := strings.Cut(rawBack, "\n")
	backPath := filepath.FromSlash(strings.TrimSpace(strings.TrimRight(backLine, "\r")))
	if backPath == "" {
		return ""
	}
	if !filepath.IsAbs(backPath) {
		backPath = filepath.Join(gitDir, backPath)
	}
	backInfo, err := os.Lstat(backPath)
	if err != nil || !os.SameFile(gitInfo, backInfo) {
		return ""
	}
	return gitDir
}

func dirHasValidGitEntry(dir string) bool {
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if info.IsDir() {
		return true
	}
	if !info.Mode().IsRegular() {
		return false
	}
	gitDir, ok := resolveGitFileTarget(dir, gitPath)
	return ok && gitDir != ""
}

func hasRegularGitmodules(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, ".gitmodules"))
	return err == nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular()
}

func populateSubmoduleMetadata(ctx context.Context, worktreeRoot, parentRepoDir string, repo *EmbeddedRepo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if repo == nil || repo.GitDir == "" || repo.Kind == EmbeddedWorktree || parentRepoDir == "" {
		return nil
	}
	if !isPathWithin(parentRepoDir, worktreeRoot) {
		return nil
	}
	if repo.Kind != EmbeddedGitFile && !hasRegularGitmodules(parentRepoDir) {
		return nil
	}
	rel, err := filepath.Rel(parentRepoDir, repo.Path)
	if err != nil {
		return nil
	}
	subRel := filepath.ToSlash(rel)
	if subRel == "" || subRel == "." || subRel == ".." || strings.HasPrefix(subRel, "../") {
		return nil
	}
	if readSubmoduleGitlink(ctx, parentRepoDir, subRel) != "" {
		repo.SuperprojectPath = parentRepoDir
		repo.SubmodulePath = subRel
	}
	return ctx.Err()
}

func parseLsTreeGitlink(output, expectedPath string) string {
	for _, entry := range strings.Split(output, "\x00") {
		if entry == "" {
			continue
		}
		meta, entryPath, ok := strings.Cut(entry, "\t")
		if !ok || entryPath != expectedPath {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) == 3 && fields[0] == "160000" && fields[1] == "commit" && isObjectID(fields[2]) {
			return fields[2]
		}
	}
	return ""
}

func parseLsFilesGitlink(output, expectedPath string) string {
	fallback := ""
	for _, entry := range strings.Split(output, "\x00") {
		if entry == "" {
			continue
		}
		meta, entryPath, ok := strings.Cut(entry, "\t")
		if !ok || entryPath != expectedPath {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) == 3 && fields[0] == "160000" && isObjectID(fields[1]) {
			if fields[2] == "0" {
				return fields[1]
			}
			if fallback == "" || fields[2] == "2" {
				fallback = fields[1]
			}
		}
	}
	return fallback
}

func readSubmoduleHeadGitlink(ctx context.Context, superprojectDir, subRelPath string) string {
	if superprojectDir == "" || subRelPath == "" || subRelPath == "." || subRelPath == ".." || strings.HasPrefix(subRelPath, "../") {
		return ""
	}
	parentGit := NewGit(superprojectDir, "file")
	res := parentGit(ctx, []string{"--no-optional-locks", "--literal-pathspecs", "ls-tree", "-z", "HEAD", "--", subRelPath}, GitOptions{Directory: superprojectDir, Whole: true})
	if res.ExitCode != 0 {
		return ""
	}
	return parseLsTreeGitlink(res.Stdout, subRelPath)
}

func readSubmoduleIndexGitlink(ctx context.Context, superprojectDir, subRelPath string) string {
	if superprojectDir == "" || subRelPath == "" || subRelPath == "." || subRelPath == ".." || strings.HasPrefix(subRelPath, "../") {
		return ""
	}
	parentGit := NewGit(superprojectDir, "file")
	res := parentGit(ctx, []string{"--no-optional-locks", "--literal-pathspecs", "ls-files", "--stage", "-z", "--", subRelPath}, GitOptions{Directory: superprojectDir, Whole: true})
	if res.ExitCode != 0 {
		return ""
	}
	return parseLsFilesGitlink(res.Stdout, subRelPath)
}

func readSubmoduleGitlink(ctx context.Context, superprojectDir, subRelPath string) string {
	if sha := readSubmoduleHeadGitlink(ctx, superprojectDir, subRelPath); sha != "" {
		return sha
	}
	if ctx.Err() != nil {
		return ""
	}
	return readSubmoduleIndexGitlink(ctx, superprojectDir, subRelPath)
}

func parseSimpleGitmodules(raw []byte, subRelPath string) (branch string, handled bool) {
	text := string(raw)
	// Fall back to `git config` for any escaped characters, line continuations, or comments/quotes in values.
	if strings.ContainsRune(text, '\\') {
		return "", false
	}
	branches := make(map[string]string)
	var matchedNames []string
	currentSub := ""
	inSub := false
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimRight(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inSub = false
			currentSub = ""
			if !strings.HasSuffix(line, "]") {
				return "", false
			}
			inner := strings.TrimSpace(line[1 : len(line)-1])
			lower := strings.ToLower(inner)
			if strings.HasPrefix(lower, "include") {
				return "", false
			}
			if !strings.HasPrefix(lower, "submodule") {
				continue
			}
			rest := strings.TrimSpace(inner[len("submodule"):])
			if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
				name := rest[1 : len(rest)-1]
				if strings.ContainsRune(name, '"') || name == "" {
					return "", false
				}
				currentSub = name
				inSub = true
				continue
			}
			return "", false
		}
		if !inSub {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		if strings.ContainsAny(val, "\"#;") {
			return "", false
		}
		switch key {
		case "path":
			cleaned := path.Clean(filepath.ToSlash(val))
			if cleaned == subRelPath {
				matchedNames = append(matchedNames, currentSub)
			}
		case "branch":
			branches[currentSub] = val
		}
	}
	for i := len(matchedNames) - 1; i >= 0; i-- {
		if b := branches[matchedNames[i]]; b != "" {
			return b, true
		}
	}
	return "", true
}

func readSubmoduleConfiguredBranch(ctx context.Context, superprojectDir, subRelPath string) string {
	if superprojectDir == "" || subRelPath == "" {
		return ""
	}
	gitmodulesPath := filepath.Join(superprojectDir, ".gitmodules")
	info, err := os.Lstat(gitmodulesPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxGitmodulesBytes {
		return ""
	}
	parentGit := NewGit(superprojectDir, "file")
	branch := ""
	handled := false
	if raw, err := os.ReadFile(gitmodulesPath); err == nil {
		branch, handled = parseSimpleGitmodules(raw, subRelPath)
	}
	if !handled {
		res := parentGit(ctx, []string{"config", "--no-includes", "--file", gitmodulesPath, "--null", "--get-regexp", `^submodule\..*\.(path|branch)$`}, GitOptions{Directory: superprojectDir, Whole: true})
		if res.ExitCode != 0 {
			return ""
		}
		branches := make(map[string]string)
		var matchedNames []string
		for _, entry := range strings.Split(res.Stdout, "\x00") {
			if entry == "" {
				continue
			}
			key, val, ok := strings.Cut(entry, "\n")
			if !ok {
				continue
			}
			rest, ok := strings.CutPrefix(key, "submodule.")
			if !ok {
				continue
			}
			if name, ok := strings.CutSuffix(rest, ".path"); ok && name != "" {
				cleaned := path.Clean(filepath.ToSlash(strings.TrimSpace(val)))
				if cleaned == subRelPath {
					matchedNames = append(matchedNames, name)
				}
			} else if name, ok := strings.CutSuffix(rest, ".branch"); ok && name != "" {
				branches[name] = strings.TrimSpace(val)
			}
		}
		for i := len(matchedNames) - 1; i >= 0; i-- {
			if b := branches[matchedNames[i]]; b != "" {
				branch = b
				break
			}
		}
	}
	if branch == "" {
		return ""
	}
	if branch == "." {
		if short, ok := readHeadBranchFast(superprojectDir, ""); ok {
			branch = short
		} else {
			sym := parentGit(ctx, []string{"symbolic-ref", "-q", "--short", "HEAD"}, GitOptions{Directory: superprojectDir})
			if sym.ExitCode != 0 {
				return ""
			}
			branch = strings.TrimSpace(sym.Stdout)
		}
	}
	branch = strings.TrimPrefix(branch, "refs/heads/")
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " \t\r\n") {
		return ""
	}
	if validRef(ctx, parentGit, "refs/heads/"+branch) != nil {
		return ""
	}
	return branch
}

func inspectEmbeddedGitFile(ctx context.Context, worktreeRoot, repoDir, relPath string, depth int, gitFile string) EmbeddedRepo {
	repo := EmbeddedRepo{
		Path:    repoDir,
		RelPath: relPath,
		Depth:   depth,
		Kind:    EmbeddedGitFile,
	}
	gitDir, ok := resolveGitFileTarget(repoDir, gitFile)
	if !ok {
		return repo
	}
	repo.GitDir = gitDir
	repo.CommonDir = gitDir

	if commonDir, ok := linkedWorktreeCommonDir(gitDir); ok {
		repo.Kind = EmbeddedWorktree
		repo.CommonDir = commonDir
		repo.RegisteredWorktreePath = lookupRegisteredWorktreePath(ctx, commonDir, gitDir, repoDir)
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

func lookupRegisteredWorktreePath(ctx context.Context, commonDir, gitDir, repoDir string) string {
	repoInfo, err := os.Lstat(repoDir)
	if err != nil || !repoInfo.IsDir() {
		return ""
	}
	if gitDir != "" {
		if rawBack, err := readSmallTextFile(filepath.Join(gitDir, "gitdir")); err == nil {
			line, _, _ := strings.Cut(rawBack, "\n")
			backPath := filepath.FromSlash(strings.TrimSpace(strings.TrimRight(line, "\r")))
			if backPath != "" {
				if !filepath.IsAbs(backPath) {
					backPath = filepath.Join(gitDir, backPath)
				}
				backPath = filepath.Clean(backPath)
				if filepath.Base(backPath) == ".git" {
					candidate := filepath.Dir(backPath)
					if info, err := os.Lstat(candidate); err == nil && info.IsDir() && os.SameFile(repoInfo, info) {
						return filepath.ToSlash(candidate)
					}
				}
			}
		}
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

func isPathWithinAny(target string, roots []string) bool {
	if target == "" || len(roots) == 0 {
		return false
	}
	resolvedTarget, err := canonicalOrClean(target)
	if err != nil {
		return false
	}
	targetKey := cleanPathKey(resolvedTarget)
	for _, root := range roots {
		if root == "" {
			continue
		}
		resolvedRoot, err := canonicalOrClean(root)
		if err != nil {
			continue
		}
		if within(targetKey, cleanPathKey(resolvedRoot)) {
			return true
		}
	}
	return false
}

func isValidGitDirectory(gitDir string) bool {
	rawHead, err := readSmallTextFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return false
	}
	headLine, _, _ := strings.Cut(rawHead, "\n")
	headLine = strings.TrimSpace(strings.TrimRight(headLine, "\r"))
	if refTarget, ok := strings.CutPrefix(headLine, "ref:"); ok {
		if strings.TrimSpace(refTarget) == "" {
			return false
		}
	} else if !isObjectID(headLine) {
		return false
	}
	commonDir := gitDir
	if rawCommon, err := readSmallTextFile(filepath.Join(gitDir, "commondir")); err == nil {
		line, _, _ := strings.Cut(rawCommon, "\n")
		target := filepath.FromSlash(strings.TrimSpace(strings.TrimRight(line, "\r")))
		if target != "" {
			if !filepath.IsAbs(target) {
				target = filepath.Join(gitDir, target)
			}
			commonDir = filepath.Clean(target)
		}
	}
	objectsInfo, err := os.Stat(filepath.Join(commonDir, "objects"))
	if err != nil || !objectsInfo.IsDir() {
		return false
	}
	if refsInfo, err := os.Stat(filepath.Join(commonDir, "refs")); err == nil && refsInfo.IsDir() {
		return true
	}
	if reftableInfo, err := os.Stat(filepath.Join(commonDir, "reftable")); err == nil && reftableInfo.IsDir() {
		return true
	}
	return false
}

func verifyEmbeddedGitDir(ctx context.Context, repoPath string, repoGit GitRun, expectedGitDir string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	expectedInfo, err := os.Stat(expectedGitDir)
	if err != nil || !expectedInfo.IsDir() {
		return false, nil
	}
	dotGit := filepath.Join(repoPath, ".git")
	dotInfo, err := os.Lstat(dotGit)
	if err != nil {
		return false, nil
	}
	if dotInfo.Mode()&os.ModeSymlink == 0 {
		actualPath := ""
		if dotInfo.IsDir() {
			actualPath = dotGit
		} else if dotInfo.Mode().IsRegular() {
			target, ok := resolveGitFileTarget(repoPath, dotGit)
			if !ok {
				return false, nil
			}
			actualPath = target
		} else {
			return false, nil
		}
		actualInfo, err := os.Stat(actualPath)
		if err != nil || !actualInfo.IsDir() || !os.SameFile(expectedInfo, actualInfo) {
			return false, nil
		}
		return isValidGitDirectory(actualPath), nil
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

func repoHasNoLocalBranches(commonDir string) bool {
	if commonDir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(commonDir, "reftable")); !os.IsNotExist(err) {
		return false
	}
	headsDir := filepath.Join(commonDir, "refs", "heads")
	hasFile := false
	err := filepath.WalkDir(headsDir, func(_ string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if !d.IsDir() {
			hasFile = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil || hasFile {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(commonDir, "packed-refs"))
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, " refs/heads/") {
			return false
		}
	}
	return true
}

func embeddedHasStash(ctx context.Context, repoGit GitRun, repo EmbeddedRepo) bool {
	commonDir := repo.CommonDir
	if commonDir == "" {
		commonDir = repo.GitDir
	}
	if commonDir != "" {
		if _, err := os.Stat(filepath.Join(commonDir, "reftable")); os.IsNotExist(err) {
			if _, err := os.Lstat(filepath.Join(commonDir, "refs", "stash")); err == nil {
				return true
			} else if !os.IsNotExist(err) {
				return repoGit(ctx, []string{"rev-parse", "--verify", "--quiet", "refs/stash"}, GitOptions{}).ExitCode == 0
			}
			raw, err := os.ReadFile(filepath.Join(commonDir, "packed-refs"))
			if os.IsNotExist(err) {
				return false
			}
			if err == nil {
				for _, line := range strings.Split(string(raw), "\n") {
					if strings.HasSuffix(strings.TrimRight(line, "\r"), " refs/stash") {
						return true
					}
				}
				return false
			}
		}
	}
	return repoGit(ctx, []string{"rev-parse", "--verify", "--quiet", "refs/stash"}, GitOptions{}).ExitCode == 0
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
	validGitDir, err := verifyEmbeddedGitDir(ctx, repo.Path, repoGit, repo.GitDir)
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

	statusArgs := append([]string{"--no-optional-locks"}, worktreeConfigArgs(ctx, repo.Path)...)
	statusArgs = append(statusArgs, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=normal")
	st := repoGit(ctx, statusArgs, GitOptions{Whole: true})
	if err := ctx.Err(); err != nil {
		return EmbeddedRepoStatus{}, err
	}
	branchRef := ""
	headSHA := ""
	if st.ExitCode != 0 {
		status.Dirty = true
		sym := repoGit(ctx, []string{"symbolic-ref", "-q", "HEAD"}, GitOptions{})
		if err := ctx.Err(); err != nil {
			return EmbeddedRepoStatus{}, err
		}
		if sym.ExitCode == 0 {
			branchRef = strings.TrimSpace(sym.Stdout)
		}
		headSHA = commitSHA(ctx, repoGit, "HEAD", "")
		if err := ctx.Err(); err != nil {
			return EmbeddedRepoStatus{}, err
		}
	} else {
		parsedBranch, parsedHead, dirty, needSymbolicFallback := parseEmbeddedInspectStatus(st.Stdout)
		branchRef = parsedBranch
		headSHA = parsedHead
		if dirty {
			status.Dirty = true
		}
		if needSymbolicFallback {
			sym := repoGit(ctx, []string{"symbolic-ref", "-q", "HEAD"}, GitOptions{})
			if err := ctx.Err(); err != nil {
				return EmbeddedRepoStatus{}, err
			}
			if sym.ExitCode == 0 {
				branchRef = strings.TrimSpace(sym.Stdout)
			}
		}
	}

	headGitlinkSHA := ""
	if repo.IsSubmodule() {
		headGitlinkSHA = readSubmoduleHeadGitlink(ctx, repo.SuperprojectPath, repo.SubmodulePath)
		if err := ctx.Err(); err != nil {
			return EmbeddedRepoStatus{}, err
		}
	}
	alignedSubmodule := repo.IsSubmodule() && headSHA != "" && headGitlinkSHA != "" && headSHA == headGitlinkSHA
	alignedDetachedSubmodule := alignedSubmodule && branchRef == ""

	if branchRef == "" && !alignedDetachedSubmodule {
		status.Dirty = true
	}

	var refSnap embeddedRefSnapshot
	if repo.ObjectStoreInsideWorktree {
		includeHead := headSHA != "" && !alignedDetachedSubmodule
		commonDir := repo.CommonDir
		if commonDir == "" {
			commonDir = repo.GitDir
		}
		if !includeHead && repoHasNoLocalBranches(commonDir) {
			if embeddedHasStash(ctx, repoGit, repo) {
				status.Unpushed = true
			}
		} else {
			refSnap = loadEmbeddedRefSnapshot(ctx, repoGit)
			if err := ctx.Err(); err != nil {
				return EmbeddedRepoStatus{}, err
			}
			resolvedUnpushed := false
			if refSnap.loaded {
				if refSnap.hasStash {
					status.Unpushed = true
					resolvedUnpushed = true
				} else {
					remoteSHAs := make(map[string]struct{})
					for name, info := range refSnap.refs {
						if !strings.HasPrefix(name, "refs/remotes/") || info.commitSHA == "" {
							continue
						}
						if strings.HasSuffix(name, "/HEAD") && info.symref != "" {
							continue
						}
						remoteSHAs[info.commitSHA] = struct{}{}
					}
					hasAnyLocal := includeHead
					allMatched := true
					ambiguous := false
					if includeHead {
						if _, ok := remoteSHAs[headSHA]; !ok {
							allMatched = false
						}
					}
					for name, info := range refSnap.refs {
						if !strings.HasPrefix(name, "refs/heads/") {
							continue
						}
						if info.commitSHA == "" {
							ambiguous = true
							break
						}
						hasAnyLocal = true
						if _, ok := remoteSHAs[info.commitSHA]; !ok {
							allMatched = false
						}
					}
					if !ambiguous {
						switch {
						case !hasAnyLocal || allMatched:
							resolvedUnpushed = true
						case len(remoteSHAs) == 0:
							status.Unpushed = true
							resolvedUnpushed = true
						}
					}
				}
			}
			if !resolvedUnpushed {
				revArgs := []string{"--no-optional-locks", "rev-list"}
				if includeHead {
					revArgs = append(revArgs, "HEAD")
				}
				revArgs = append(revArgs, "--branches", "--not", "--remotes", "--max-count=1")
				unpushed := repoGit(ctx, revArgs, GitOptions{})
				if err := ctx.Err(); err != nil {
					return EmbeddedRepoStatus{}, err
				}
				if unpushed.ExitCode != 0 || strings.TrimSpace(unpushed.Stdout) != "" {
					status.Unpushed = true
				} else if (!refSnap.loaded && embeddedHasStash(ctx, repoGit, repo)) || refSnap.hasStash {
					status.Unpushed = true
				}
			}
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
	case alignedSubmodule:
		status.Unmerged = false
	case branchRef == "":
		status.Unmerged = true
	default:
		if !refSnap.loaded {
			refSnap = loadEmbeddedRefSnapshot(ctx, repoGit)
			if err := ctx.Err(); err != nil {
				return EmbeddedRepoStatus{}, err
			}
		}
		status.Unmerged = !embeddedBranchMerged(ctx, repoGit, repo, branchRef, headSHA, &refSnap)
	}
	if err := ctx.Err(); err != nil {
		return EmbeddedRepoStatus{}, err
	}
	return status, nil
}

func parseEmbeddedInspectStatus(output string) (branchRef, headSHA string, dirty, needSymbolicFallback bool) {
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
					headSHA = oid
				}
			} else if head, ok := strings.CutPrefix(rec, "# branch.head "); ok {
				head = strings.TrimSpace(head)
				switch {
				case head == "", head == "(detached)":
					branchRef = ""
				case strings.EqualFold(head, "(null)"), strings.EqualFold(head, "(unknown)"), strings.HasPrefix(head, "refs/"):
					branchRef = ""
					needSymbolicFallback = true
				default:
					branchRef = "refs/heads/" + head
				}
			}
			continue
		}
		dirty = true
		break
	}
	return branchRef, headSHA, dirty, needSymbolicFallback
}

type embeddedRefInfo struct {
	commitSHA string
	symref    string
	upstream  string
}

type embeddedRefSnapshot struct {
	loaded      bool
	refs        map[string]embeddedRefInfo
	remoteHeads []string
	hasStash    bool
}

func loadEmbeddedRefSnapshot(ctx context.Context, git GitRun) embeddedRefSnapshot {
	res := git(ctx, []string{
		"--no-optional-locks",
		"for-each-ref",
		"--format=%(refname)%09%(objectname)%09%(objecttype)%09%(*objectname)%09%(*objecttype)%09%(symref)%09%(upstream)",
		"refs/heads", "refs/remotes", "refs/stash",
	}, GitOptions{Whole: true})
	if res.ExitCode != 0 {
		return embeddedRefSnapshot{}
	}
	snap := embeddedRefSnapshot{
		loaded: true,
		refs:   make(map[string]embeddedRefInfo),
	}
	for _, rawLine := range strings.Split(res.Stdout, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 7 {
			continue
		}
		name := parts[0]
		if name == "" {
			continue
		}
		if name == "refs/stash" {
			snap.hasStash = true
			continue
		}
		sha := ""
		if parts[2] == "commit" {
			sha = parts[1]
		} else if parts[4] == "commit" {
			sha = parts[3]
		}
		snap.refs[name] = embeddedRefInfo{
			commitSHA: sha,
			symref:    parts[5],
			upstream:  parts[6],
		}
		if strings.HasPrefix(name, "refs/remotes/") && strings.HasSuffix(name, "/HEAD") {
			rest := strings.TrimPrefix(name, "refs/remotes/")
			if strings.Count(rest, "/") == 1 {
				snap.remoteHeads = append(snap.remoteHeads, name)
			}
		}
	}
	return snap
}

func (s *embeddedRefSnapshot) commitSHA(ctx context.Context, git GitRun, targetRef string) string {
	if targetRef == "" {
		return ""
	}
	if s != nil && s.loaded && (strings.HasPrefix(targetRef, "refs/heads/") || strings.HasPrefix(targetRef, "refs/remotes/")) {
		return s.refs[targetRef].commitSHA
	}
	return commitSHA(ctx, git, targetRef, "")
}

func (s *embeddedRefSnapshot) resolveHeadUpstream(ctx context.Context, git GitRun, branchRef string) string {
	if branchRef == "" {
		return ""
	}
	if s == nil || !s.loaded {
		return resolveUpstreamRef(ctx, git, "")
	}
	upstream := s.refs[branchRef].upstream
	if upstream == "" {
		return ""
	}
	if _, exists := s.refs[upstream]; exists {
		return upstream
	}
	if strings.HasPrefix(upstream, "refs/heads/") || strings.HasPrefix(upstream, "refs/remotes/") {
		return ""
	}
	if commitSHA(ctx, git, upstream, "") != "" {
		return upstream
	}
	return ""
}

func (s *embeddedRefSnapshot) resolveBranchUpstream(ctx context.Context, git GitRun, branchRef string) string {
	if branchRef == "" {
		return ""
	}
	if s == nil || !s.loaded {
		return resolveUpstreamRef(ctx, git, branchRef)
	}
	return s.refs[branchRef].upstream
}

func (s *embeddedRefSnapshot) resolveRemoteDefaultRef(ctx context.Context, git GitRun, preferredRemote string) string {
	if s == nil || !s.loaded {
		return resolveRemoteDefaultRef(ctx, git, preferredRemote)
	}
	if preferredRemote != "" {
		if ref := s.refs["refs/remotes/"+preferredRemote+"/HEAD"].symref; ref != "" {
			return ref
		}
	}
	if preferredRemote != "origin" {
		if ref := s.refs["refs/remotes/origin/HEAD"].symref; ref != "" {
			return ref
		}
	}
	for _, name := range s.remoteHeads {
		if ref := s.refs[name].symref; ref != "" {
			return ref
		}
	}
	return ""
}

func (s *embeddedRefSnapshot) soleRemoteName(ctx context.Context, git GitRun) string {
	if s == nil || !s.loaded {
		return soleRemoteName(ctx, git)
	}
	sole := ""
	for name := range s.refs {
		remote, _, ok := remoteNameAndBranch(name)
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

func (s *embeddedRefSnapshot) isPureSingleBranchTracking(ctx context.Context, git GitRun, repo EmbeddedRepo, branchRef, upstreamRef, upstreamRemote, hostRef string, hostHasCommit bool) bool {
	if s == nil || !s.loaded {
		return isPureSingleBranchTracking(ctx, git, repo, branchRef, upstreamRef, upstreamRemote, hostRef, hostHasCommit)
	}
	if !repo.ObjectStoreInsideWorktree {
		if hostHasCommit && hostRef != "" && hostRef != branchRef {
			return false
		}
	} else {
		for name := range s.refs {
			if strings.HasPrefix(name, "refs/heads/") && name != branchRef {
				return false
			}
		}
	}
	branchShort := strings.TrimPrefix(branchRef, "refs/heads/")
	upstreamPrefix := "refs/remotes/" + upstreamRemote + "/"
	for name := range s.refs {
		if !strings.HasPrefix(name, "refs/remotes/") || strings.HasSuffix(name, "/HEAD") {
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

func embeddedBranchMerged(ctx context.Context, git GitRun, repo EmbeddedRepo, branchRef, headSHA string, snap *embeddedRefSnapshot) bool {
	ancestorCache := make(map[string]bool)
	isAncestor := func(targetRef string) bool {
		if targetRef == "" || targetRef == branchRef {
			return false
		}
		targetSHA := snap.commitSHA(ctx, git, targetRef)
		if targetSHA == "" {
			return false
		}
		if targetSHA == headSHA {
			return true
		}
		if cached, ok := ancestorCache[targetSHA]; ok {
			return cached
		}
		ok := git(ctx, []string{"--no-optional-locks", "merge-base", "--is-ancestor", headSHA, targetSHA}, GitOptions{}).ExitCode == 0
		ancestorCache[targetSHA] = ok
		return ok
	}

	upstreamRef := snap.resolveHeadUpstream(ctx, git, branchRef)
	upstreamRemote, upstreamShort, hasRemoteUpstream := remoteNameAndBranch(upstreamRef)

	// 1. Explicit remote default branch (refs/remotes/<remote>/HEAD), preferring
	//    the current branch's remote, then origin, then any remote HEAD.
	remoteDefault := snap.resolveRemoteDefaultRef(ctx, git, upstreamRemote)
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

	// 2. For external object stores (linked worktrees sharing a host git dir),
	//    check the host repo's HEAD branch and its remote counterpart.
	hostRef := ""
	hostHasCommit := false
	if !repo.ObjectStoreInsideWorktree && !repo.IsSubmodule() {
		hostRef = readCommonHeadRef(repo.CommonDir)
		if hostRef != "" {
			if snap.commitSHA(ctx, git, hostRef) != "" {
				hostHasCommit = true
			}
			if isAncestor(hostRef) {
				return true
			}
			if hostUpstream := snap.resolveBranchUpstream(ctx, git, hostRef); isAncestor(hostUpstream) {
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
		if snap.commitSHA(ctx, git, candidate) == "" {
			return false
		}
		hasExplicitDefaultCandidate = true
		return isAncestor(candidate)
	}

	if repo.IsSubmodule() {
		if configuredBranch := readSubmoduleConfiguredBranch(ctx, repo.SuperprojectPath, repo.SubmodulePath); configuredBranch != "" {
			if hasRemoteUpstream && checkCandidate("refs/remotes/"+upstreamRemote+"/"+configuredBranch) {
				return true
			}
			if checkCandidate("refs/remotes/origin/" + configuredBranch) {
				return true
			}
			if onlyRemote := snap.soleRemoteName(ctx, git); onlyRemote != "" {
				if checkCandidate("refs/remotes/" + onlyRemote + "/" + configuredBranch) {
					return true
				}
			}
			if !repo.ObjectStoreInsideWorktree && checkCandidate("refs/heads/"+configuredBranch) {
				return true
			}
		}
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
		if onlyRemote := snap.soleRemoteName(ctx, git); onlyRemote != "" {
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
			if snap.isPureSingleBranchTracking(ctx, git, repo, branchRef, upstreamRef, upstreamRemote, hostRef, hostHasCommit) {
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
		if hasBlockingGitMarkers(repo.GitDir, repo.Kind == EmbeddedWorktree) {
			return false, nil
		}
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

func hasDirectSubmodule(repos []EmbeddedRepo, worktreeDir string) bool {
	targetKey := worktreePathKey(worktreeDir)
	if targetKey == "" {
		return false
	}
	for _, repo := range repos {
		if repo.IsSubmodule() && worktreePathKey(repo.SuperprojectPath) == targetKey {
			return true
		}
	}
	return false
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
		} else if hasDirectSubmodule(repos, repo.Path) {
			args = append(args, "--force")
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
		if strings.Contains(lower, "modified or untracked") || strings.Contains(lower, "use --force") || strings.Contains(lower, "locked") || strings.Contains(lower, "submodules") {
			return false, nil
		}
		return false, gitFailure(result, "git worktree remove")
	}
	return true, nil
}
