package local

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/fsutil"
	"github.com/daidr/Ariadne/sdk"
)

const WorktreeDirectory = ".ariadne-worktrees"

type FactoryOptions struct {
	Path, DataDir string
	// nil detects git; an explicit empty string chooses a shared plain directory.
	RepositoryRoot *string
	Clone          Cloner
	DisableClone   bool
}
type OpenRequest struct {
	ThreadID string
	Branch   *string
	// nil means choose the checkout's current branch; a pointer to nil records
	// that the workspace began at detached HEAD and has no base branch.
	Base **string
}
type threadLock struct {
	token chan struct{}
	users int
}
type Workspaces struct {
	path, repository, common string
	roots                    []string
	git                      GitRun
	source                   *Workspace
	clone                    Cloner
	life                     context.Context
	cancel                   context.CancelFunc
	mu                       sync.Mutex
	closed                   bool
	closeDone                chan struct{}
	jobs                     sync.WaitGroup
	active                   map[*Workspace]struct{}
	locksMu                  sync.Mutex
	locks                    map[string]*threadLock
	cloneMu                  chan struct{}
	cloneChecked             bool
	cloneRoot                string
}

func NewWorkspaces(ctx context.Context, options FactoryOptions) (*Workspaces, error) {
	source, err := NewWorkspace(options.Path, WorkspaceOptions{ID: "source", HideGit: true})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Workspaces, error) { source.Close(); return nil, err }
	repository := ""
	if options.RepositoryRoot == nil {
		repository = GitRoot(ctx, source.directory)
	} else {
		repository = *options.RepositoryRoot
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	common := ""
	var git GitRun
	var roots []string
	if repository != "" {
		repository, err = filepath.Abs(repository)
		if err != nil {
			return fail(err)
		}
		repository, err = filepath.EvalSymlinks(repository)
		if err != nil {
			return fail(err)
		}
		if !within(source.directory, repository) {
			return fail(errors.New("project path is outside the repository"))
		}
		git = NewGit(repository, "file")
		result := git(ctx, []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}, GitOptions{})
		if result.ExitCode != 0 {
			return fail(gitFailure(result, "git rev-parse"))
		}
		common, err = filepath.EvalSymlinks(strings.TrimSpace(result.Stdout))
		if err != nil {
			return fail(err)
		}
		data, err := filepath.Abs(options.DataDir)
		if err != nil {
			return fail(err)
		}
		if options.DataDir == "" {
			return fail(errors.New("workspaces require a data directory"))
		}
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(repository)))[:12]
		roots = []string{filepath.Join(data, "worktrees", key), filepath.Join(filepath.Dir(repository), WorktreeDirectory, key)}
	}
	clone := options.Clone
	if clone == nil && !options.DisableClone {
		clone = PlatformCloner
	}
	if options.DisableClone {
		clone = nil
	}
	factory := newWorkspaceOwner()
	factory.path, factory.repository, factory.common = source.directory, repository, common
	factory.roots, factory.git, factory.source, factory.clone = roots, git, source, clone
	return factory, nil
}
func newWorkspaceOwner() *Workspaces {
	life, cancel := context.WithCancel(context.Background())
	return &Workspaces{life: life, cancel: cancel, active: map[*Workspace]struct{}{}, locks: map[string]*threadLock{}, closeDone: make(chan struct{}), cloneMu: make(chan struct{}, 1)}
}
func (factory *Workspaces) Isolated() bool { return factory.repository != "" }
func (factory *Workspaces) View() sdk.SourceView {
	if factory.source == nil {
		return nil
	}
	return SearchableView(factory.source, "")
}
func BranchFor(threadID string) string {
	tail := []rune(threadID)
	if len(tail) > 12 {
		tail = tail[len(tail)-12:]
	}
	return "ariadne/" + string(tail)
}
func safeThreadID(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 240 || strings.ContainsAny(id, "/\\\x00:") {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func (factory *Workspaces) begin(ctx context.Context) (context.Context, func(), error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if factory.closed {
		return nil, nil, errors.New("workspaces are closed")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(factory.life, cancel)
	factory.jobs.Add(1)
	return ctx, func() { stop(); cancel(); factory.jobs.Done() }, nil
}
func (factory *Workspaces) thread(ctx context.Context, id string) (func(), error) {
	if !safeThreadID(id) {
		return nil, errors.New("invalid workspace thread id")
	}
	factory.locksMu.Lock()
	entry := factory.locks[id]
	if entry == nil {
		entry = &threadLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		factory.locks[id] = entry
	}
	entry.users++
	factory.locksMu.Unlock()
	release := func() {
		factory.locksMu.Lock()
		entry.users--
		if entry.users == 0 {
			delete(factory.locks, id)
		}
		factory.locksMu.Unlock()
	}
	select {
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	case <-entry.token:
	}
	return func() { entry.token <- struct{}{}; release() }, nil
}
func (factory *Workspaces) directory(threadID string) string {
	for _, root := range factory.roots {
		dir := filepath.Join(root, threadID)
		if _, err := os.Lstat(dir); err == nil {
			return dir
		}
	}
	return filepath.Join(factory.roots[0], threadID)
}
func (factory *Workspaces) checkedDirectory(threadID string) (string, error) {
	if !safeThreadID(threadID) {
		return "", errors.New("invalid workspace thread id")
	}
	if !factory.Isolated() {
		return "", errors.New("project has no separate git workspaces")
	}
	dir := factory.directory(threadID)
	if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("worktree directory must not be a link")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if os.IsNotExist(err) {
		return dir, nil
	}
	if err != nil {
		return "", err
	}
	for _, root := range factory.roots {
		parent, err := filepath.EvalSymlinks(root)
		if err == nil && resolved == filepath.Join(parent, threadID) {
			return resolved, nil
		}
	}
	return "", errors.New("worktree directory is outside its owned root")
}
func (factory *Workspaces) verifiedWorktree(ctx context.Context, directory string) error {
	result := factory.git(ctx, []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}, GitOptions{Directory: directory})
	if result.ExitCode != 0 {
		return gitFailure(result, "git rev-parse")
	}
	common, err := filepath.EvalSymlinks(strings.TrimSpace(result.Stdout))
	if err != nil {
		return err
	}
	actual, err := os.Stat(common)
	if err != nil {
		return err
	}
	expected, err := os.Stat(factory.common)
	if err != nil {
		return err
	}
	if !os.SameFile(actual, expected) {
		return errors.New("worktree belongs to a different repository")
	}
	return nil
}
func (factory *Workspaces) cloningRoot(ctx context.Context) (string, error) {
	select {
	case factory.cloneMu <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-factory.cloneMu }()
	if factory.cloneChecked {
		return factory.cloneRoot, nil
	}
	if factory.clone == nil {
		factory.cloneChecked = true
		return "", nil
	}
	for _, root := range factory.roots {
		_, err := os.Lstat(root)
		fresh := os.IsNotExist(err)
		if CanClone(ctx, factory.clone, factory.common, root) {
			factory.cloneRoot = root
			factory.cloneChecked = true
			return root, nil
		}
		if fresh {
			_ = os.Remove(root)
			_ = os.Remove(filepath.Dir(root))
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	factory.cloneChecked = true
	return "", nil
}
func (factory *Workspaces) Open(ctx context.Context, input OpenRequest) (*Workspace, error) {
	ctx, done, err := factory.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	release, err := factory.thread(ctx, input.ThreadID)
	if err != nil {
		return nil, err
	}
	defer release()
	var branch, base *string
	root := factory.path
	if factory.Isolated() {
		chosen := BranchFor(input.ThreadID)
		if input.Branch != nil {
			chosen = *input.Branch
		}
		if err := validRef(ctx, factory.git, "refs/heads/"+chosen); err != nil {
			return nil, err
		}
		branch = &chosen
		if input.Base != nil {
			if *input.Base != nil {
				base = new(**input.Base)
			}
		} else {
			result := factory.git(ctx, []string{"symbolic-ref", "--quiet", "--short", "HEAD"}, GitOptions{})
			if result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
				base = new(strings.TrimSpace(result.Stdout))
			}
		}
		dir, err := factory.checkedDirectory(input.ThreadID)
		if err != nil {
			return nil, err
		}
		_, err = os.Lstat(filepath.Join(dir, ".git"))
		fresh := os.IsNotExist(err)
		if err != nil && !fresh {
			return nil, err
		}
		if fresh {
			shared, err := factory.cloningRoot(ctx)
			if err != nil {
				return nil, err
			}
			if shared != "" {
				dir = filepath.Join(shared, input.ThreadID)
			}
			if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
				return nil, err
			}
			pruned := factory.git(ctx, []string{"worktree", "prune"}, GitOptions{})
			if pruned.ExitCode != 0 {
				return nil, gitFailure(pruned, "git worktree prune")
			}
			hasBranch := commitSHA(ctx, factory.git, "refs/heads/"+chosen, "") != ""
			args := []string{"worktree", "add"}
			if shared != "" {
				args = append(args, "--no-checkout")
			}
			if hasBranch {
				args = append(args, "--", dir, chosen)
			} else {
				args = append(args, "-b", chosen, "--", dir, "HEAD")
			}
			result := factory.git(ctx, args, GitOptions{})
			if result.ExitCode != 0 {
				return nil, gitFailure(result, "git worktree add")
			}
		}
		if err := factory.verifiedWorktree(ctx, dir); err != nil {
			return nil, err
		}
		pending, err := CheckoutPending(ctx, factory.git, dir)
		if err != nil {
			return nil, err
		}
		if pending {
			if _, err := CheckoutCloned(ctx, factory.repository, dir, factory.git, factory.clone); err != nil {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				factory.git(cleanup, []string{"worktree", "remove", "--force", "--force", "--", dir}, GitOptions{Timeout: 10 * time.Second})
				stop()
				return nil, err
			}
			if _, err := SeedWorktree(ctx, factory.repository, dir, factory.git, factory.clone); err != nil {
				return nil, err
			}
		}
		sub, err := filepath.Rel(factory.repository, factory.path)
		if err != nil {
			return nil, err
		}
		root = filepath.Join(dir, sub)
		if _, err := os.Stat(root); err != nil {
			return nil, fmt.Errorf("%s is not in the repository's current commit (untracked or ignored), so the thread's checkout has no such directory; commit it, or choose the repository root", sub)
		}
	}
	workspace, err := NewWorkspace(root, WorkspaceOptions{ID: input.ThreadID, Branch: branch, Base: base})
	if err != nil {
		return nil, err
	}
	return factory.track(ctx, workspace)
}
func (factory *Workspaces) track(ctx context.Context, workspace *Workspace) (*Workspace, error) {
	factory.mu.Lock()
	if factory.closed || ctx.Err() != nil {
		factory.mu.Unlock()
		workspace.Close()
		return nil, context.Canceled
	}
	workspace.onClose = func() { factory.mu.Lock(); delete(factory.active, workspace); factory.mu.Unlock() }
	factory.active[workspace] = struct{}{}
	factory.mu.Unlock()
	return workspace, nil
}
func (factory *Workspaces) SharesStorage(ctx context.Context) (bool, error) {
	ctx, done, err := factory.begin(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	if !factory.Isolated() {
		return false, nil
	}
	root, err := factory.cloningRoot(ctx)
	return root != "", err
}
func (factory *Workspaces) Remove(ctx context.Context, threadID string, options RemoveOptions) (bool, error) {
	return factory.remove(ctx, threadID, options, "")
}

func (factory *Workspaces) RemoveOrphaned(ctx context.Context, threadID, label string) (bool, error) {
	if !filepath.IsAbs(label) {
		return false, nil
	}
	return factory.remove(ctx, threadID, RemoveOptions{}, label)
}

func (factory *Workspaces) remove(ctx context.Context, threadID string, options RemoveOptions, orphanLabel string) (bool, error) {
	ctx, done, err := factory.begin(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	release, err := factory.thread(ctx, threadID)
	if err != nil {
		return false, err
	}
	defer release()
	dir, err := factory.checkedDirectory(threadID)
	if err != nil {
		return false, err
	}
	factory.mu.Lock()
	owned := []*Workspace{}
	for workspace := range factory.active {
		if workspace.options.ID == threadID {
			owned = append(owned, workspace)
		}
	}
	factory.mu.Unlock()
	for _, workspace := range owned {
		if err := workspace.Close(); err != nil {
			return false, err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if orphanLabel != "" {
		if info == nil || !info.IsDir() {
			return false, nil
		}
		return factory.removeOrphanedDirectory(ctx, dir, orphanLabel, info)
	}
	if info != nil {
		// A previous Git removal can leave files without their checkout metadata.
		// Only the explicitly confirmed orphan cleanup can remove that directory.
		if _, err := os.Lstat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		if err := factory.verifiedWorktree(ctx, dir); err != nil {
			return false, err
		}
	}
	removed, err := RemoveWorktree(ctx, factory.git, dir, options)
	if err == nil && removed && options.Discard {
		err = DropSnapshots(ctx, factory.git, threadID)
	}
	return removed, err
}

func (factory *Workspaces) removeOrphanedDirectory(ctx context.Context, dir, label string, checked os.FileInfo) (bool, error) {
	subdir, err := filepath.Rel(factory.repository, factory.path)
	if err != nil || !filepath.IsLocal(subdir) || worktreePathKey(filepath.Join(dir, subdir)) != worktreePathKey(label) {
		return false, nil
	}
	root, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return false, err
	}
	defer root.Close()
	name := filepath.Base(dir)
	if _, err := root.Lstat(filepath.Join(name, ".git")); !os.IsNotExist(err) {
		return false, err
	}
	paths, err := registeredWorktrees(ctx, factory.git)
	if err != nil {
		return false, err
	}
	for _, path := range paths {
		if worktreePathKey(path) == worktreePathKey(dir) {
			return false, nil
		}
		info, err := os.Stat(path)
		if err != nil && !os.IsNotExist(err) {
			return false, err
		}
		if err == nil && os.SameFile(checked, info) {
			return false, nil
		}
	}
	current, err := root.Lstat(name)
	if err != nil {
		return false, err
	}
	if !current.IsDir() || !os.SameFile(checked, current) {
		return false, nil
	}
	if _, err := root.Lstat(filepath.Join(name, ".git")); !os.IsNotExist(err) {
		return false, err
	}
	if err := fsutil.RemoveTreeAt(ctx, root, name); err != nil {
		return false, err
	}
	return true, nil
}
func (factory *Workspaces) Snapshot(ctx context.Context, threadID string) (*string, error) {
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
	tree, err := WorktreeSnapshot(ctx, factory.git, dir, threadID)
	if err != nil {
		return nil, err
	}
	return &tree, nil
}
func (factory *Workspaces) revisions(ctx context.Context, branch, baseRef string) (sdk.BranchRevisions, error) {
	ctx, done, err := factory.begin(ctx)
	if err != nil {
		return sdk.BranchRevisions{}, err
	}
	defer done()
	return branchRevisions(ctx, factory.git, branch, baseRef)
}

func (factory *Workspaces) Changes(ctx context.Context, threadID, branch, base string) (*sdk.BranchChanges, error) {
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
		dir = ""
	} else if err != nil {
		return nil, err
	}
	return BranchChanges(ctx, factory.git, BranchInput{Branch: branch, Base: base, BaseRef: "refs/heads/" + base, Worktree: dir})
}
func registeredWorktrees(ctx context.Context, git GitRun) ([]string, error) {
	fields, err := worktreeFields(ctx, git)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, field := range fields {
		if path, ok := strings.CutPrefix(field, "worktree "); ok {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func worktreePathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func (factory *Workspaces) Worktrees(ctx context.Context, measure bool) ([]sdk.WorktreeInfo, error) {
	ctx, done, err := factory.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	result := []sdk.WorktreeInfo{}
	if !factory.Isolated() {
		return result, nil
	}
	paths, err := registeredWorktrees(ctx, factory.git)
	if err != nil {
		return nil, err
	}
	registered := map[string]bool{}
	for _, path := range paths {
		registered[worktreePathKey(path)] = true
	}
	for _, root := range factory.roots {
		resolved, err := filepath.EvalSymlinks(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(resolved)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !entry.IsDir() || !safeThreadID(entry.Name()) {
				continue
			}
			path := filepath.Join(root, entry.Name())
			canonical := filepath.Join(resolved, entry.Name())
			listed := registered[worktreePathKey(path)] || registered[worktreePathKey(canonical)]
			if !listed && !strings.HasPrefix(entry.Name(), "thr_") {
				continue
			}
			// A registration can outlive the checkout. Require its local Git
			// pointer. The manager checks recorded ownership of unregistered paths.
			info, err := os.Lstat(filepath.Join(canonical, ".git"))
			if os.IsNotExist(err) {
				if !listed {
					result = append(result, sdk.WorktreeInfo{ThreadID: entry.Name(), Path: canonical, Orphaned: true})
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			if !listed || !info.Mode().IsRegular() {
				continue
			}
			result = append(result, sdk.WorktreeInfo{ThreadID: entry.Name(), Path: path})
		}
	}
	if measure {
		// DiskUsage limits concurrent walks globally. Bound the workers here too
		// so a large list does not queue a separate goroutine for every directory.
		workers := min(4, len(result))
		var scans sync.WaitGroup
		for worker := range workers {
			scans.Go(func() {
				for i := worker; i < len(result) && ctx.Err() == nil; i += workers {
					result[i].Bytes = fsutil.DiskUsage(ctx, result[i].Path)
				}
			})
		}
		scans.Wait()
	}
	return result, ctx.Err()
}
func (factory *Workspaces) Close() error {
	factory.mu.Lock()
	if factory.closed {
		factory.mu.Unlock()
		<-factory.closeDone
		return nil
	}
	factory.closed = true
	factory.cancel()
	factory.mu.Unlock()
	defer close(factory.closeDone)
	factory.jobs.Wait()
	factory.mu.Lock()
	active := []*Workspace{}
	for workspace := range factory.active {
		active = append(active, workspace)
	}
	factory.mu.Unlock()
	var err error
	for _, workspace := range active {
		err = errors.Join(err, workspace.Close())
	}
	if factory.source != nil {
		err = errors.Join(err, factory.source.Close())
	}
	return err
}
