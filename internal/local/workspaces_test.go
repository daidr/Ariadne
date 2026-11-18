package local

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

func newFactory(t *testing.T, options FactoryOptions) *Workspaces {
	t.Helper()
	factory, err := NewWorkspaces(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := factory.Close(); err != nil {
			t.Error(err)
		}
	})
	return factory
}
func TestPlainWorkspacesShareFilesAndReleaseClosedHandles(t *testing.T) {
	root := t.TempDir()
	empty := ""
	factory := newFactory(t, FactoryOptions{Path: root, RepositoryRoot: &empty})
	if factory.Isolated() {
		t.Fatal("plain directory isolated")
	}
	one, err := factory.Open(t.Context(), OpenRequest{ThreadID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := factory.Open(t.Context(), OpenRequest{ThreadID: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if err := one.WriteFile(t.Context(), "value.txt", "shared"); err != nil {
		t.Fatal(err)
	}
	if value, err := two.ReadFile(t.Context(), "value.txt"); err != nil || value != "shared" {
		t.Fatal(value, err)
	}
	one.Close()
	two.Close()
	factory.mu.Lock()
	count := len(factory.active)
	factory.mu.Unlock()
	if count != 0 {
		t.Fatal("closed workspace retained", count)
	}
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := factory.Open(t.Context(), OpenRequest{ThreadID: "closed"}); err == nil {
		t.Fatal("closed factory opened workspace")
	}
}
func TestRepositoryWorkspacesPreserveBaseAndSubdirectory(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	writeGit(t, root, "src/code.txt", "committed\n")
	gitCommit(t, git, "source")
	writeGit(t, root, "src/code.txt", "user dirty\n")
	factory := newFactory(t, FactoryOptions{Path: filepath.Join(root, "src"), DataDir: t.TempDir(), DisableClone: true})
	if !factory.Isolated() {
		t.Fatal("repository not isolated")
	}
	if data, err := factory.View().ReadBytes(t.Context(), "code.txt", 1024); err != nil || string(data) != "user dirty\n" {
		t.Fatal(string(data), err)
	}
	workspace, err := factory.Open(t.Context(), OpenRequest{ThreadID: "thread_123456789012"})
	if err != nil {
		t.Fatal(err)
	}
	info := workspace.Info()
	if info.Branch == nil || *info.Branch != "ariadne/123456789012" || info.Base == nil || *info.Base != "main" || filepath.Base(info.Label) != "src" {
		t.Fatal(info)
	}
	if value, err := workspace.ReadFile(t.Context(), "code.txt"); err != nil || value != "committed\n" {
		t.Fatal(value, err)
	}
	if err := workspace.WriteFile(t.Context(), "code.txt", "thread change\n"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "src", "code.txt")); string(data) != "user dirty\n" {
		t.Fatal("thread changed source", string(data))
	}
	recorded := info.Base
	workspace.Close()
	gitOK(t, git, "checkout", "-qb", "prerelease")
	resumed, err := factory.Open(t.Context(), OpenRequest{ThreadID: info.ID, Branch: info.Branch, Base: &recorded})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Info().Base == nil || *resumed.Info().Base != "main" {
		t.Fatal("recorded base changed")
	}
	if value, err := resumed.ReadFile(t.Context(), "code.txt"); err != nil || value != "thread change\n" {
		t.Fatal(value, err)
	}
	removed, err := factory.Remove(t.Context(), info.ID, RemoveOptions{Spare: true})
	if err != nil || removed {
		t.Fatal("uncommitted work removed", removed, err)
	}
	removed, err = factory.Remove(t.Context(), info.ID, RemoveOptions{Discard: true})
	if err != nil || !removed {
		t.Fatal(removed, err)
	}
	if commitSHA(t.Context(), git, "refs/heads/"+*info.Branch, "") == "" {
		t.Fatal("thread branch lost")
	}
}
func TestConcurrentWorkspaceOpenCoalescesCreation(t *testing.T) {
	t.Parallel()
	root, _ := initGit(t)
	factory := newFactory(t, FactoryOptions{Path: root, DataDir: t.TempDir(), DisableClone: true})
	var jobs sync.WaitGroup
	results := make(chan *Workspace, 6)
	failures := make(chan error, 6)
	for range 6 {
		jobs.Go(func() {
			workspace, err := factory.Open(t.Context(), OpenRequest{ThreadID: "same_thread"})
			if err != nil {
				failures <- err
			} else {
				results <- workspace
			}
		})
	}
	jobs.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	paths := map[string]bool{}
	for workspace := range results {
		paths[workspace.directory] = true
		workspace.Close()
	}
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	factory.locksMu.Lock()
	count := len(factory.locks)
	factory.locksMu.Unlock()
	if count != 0 {
		t.Fatal("operation locks retained", count)
	}
	if t.Failed() {
		return
	}
	if removed, err := factory.Remove(t.Context(), "same_thread", RemoveOptions{}); err != nil || !removed {
		t.Fatal(removed, err)
	}
}
func TestRemoveWorkspaceWaitsForCommandAndDropsSnapshots(t *testing.T) {
	t.Parallel()
	root, _ := initGit(t)
	factory := newFactory(t, FactoryOptions{Path: root, DataDir: t.TempDir(), DisableClone: true})
	workspace, err := factory.Open(t.Context(), OpenRequest{ThreadID: "active_thread"})
	if err != nil {
		t.Fatal(err)
	}
	if tree, err := factory.Snapshot(t.Context(), "active_thread"); err != nil || tree == nil {
		t.Fatal(tree, err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(workspace.directory, "ready")
	finished := make(chan sdk.ExecResult, 1)
	go func() {
		out, _ := workspace.ExecFile(t.Context(), []string{program, "-test.run=^TestWorkspaceCommandChild$", "--", "--workspace-child", ready}, sdk.ExecOptions{})
		finished <- out
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not start")
		}
		time.Sleep(time.Millisecond)
	}
	removed, err := factory.Remove(t.Context(), "active_thread", RemoveOptions{Discard: true})
	if err != nil || !removed {
		t.Fatal(removed, err)
	}
	select {
	case result := <-finished:
		if result.ExitCode != 130 {
			t.Fatal(result)
		}
	case <-time.After(time.Second):
		t.Fatal("remove returned before command exited")
	}
	if refs := gitOK(t, factory.git, "for-each-ref", "--format=%(refname)", SnapshotRefs("active_thread")); refs != "" {
		t.Fatal(refs)
	}
}
func fakeClone(ctx context.Context, from, to string) bool {
	if ctx.Err() != nil {
		return false
	}
	info, err := os.Stat(from)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return os.CopyFS(to, os.DirFS(from)) == nil
	}
	data, err := os.ReadFile(from)
	return err == nil && os.WriteFile(to, data, info.Mode().Perm()) == nil
}
func TestClonedCheckoutUsesCommittedFilesAndSeedsDependencies(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	writeGit(t, root, ".gitignore", "node_modules/\nvendor/\n.env\n")
	gitCommit(t, git, "ignore")
	writeGit(t, root, "file.txt", "dirty source\n")
	writeGit(t, root, "node_modules/dependency/index.js", "dependency")
	writeGit(t, root, "vendor/module/mod.go", "vendor")
	writeGit(t, root, ".env", "secret")
	factory := newFactory(t, FactoryOptions{Path: root, DataDir: t.TempDir(), Clone: fakeClone})
	workspace, err := factory.Open(t.Context(), OpenRequest{ThreadID: "cloned_thread"})
	if err != nil {
		t.Fatal(err)
	}
	if value, err := workspace.ReadFile(t.Context(), "file.txt"); err != nil || value != "base\n" {
		t.Fatal("dirty source copied into checkout", value, err)
	}
	if value, err := workspace.ReadFile(t.Context(), "node_modules/dependency/index.js"); err != nil || value != "dependency" {
		t.Fatal(value, err)
	}
	if value, err := workspace.ReadFile(t.Context(), "vendor/module/mod.go"); err != nil || value != "vendor" {
		t.Fatal(value, err)
	}
	if _, err := os.Stat(filepath.Join(workspace.directory, ".env")); !os.IsNotExist(err) {
		t.Fatal("ignored user data copied", err)
	}
	if pending, err := CheckoutPending(t.Context(), factory.git, workspace.directory); err != nil || pending {
		t.Fatal(pending, err)
	}
	if removed, err := factory.Remove(t.Context(), "cloned_thread", RemoveOptions{Spare: true}); err != nil || !removed {
		t.Fatal("dependency directories prevented cleanup", removed, err)
	}
}
func TestFailedDependencyCloneIsRemoved(t *testing.T) {
	t.Parallel()
	root, git := initGit(t)
	writeGit(t, root, ".gitignore", "node_modules/\n")
	gitCommit(t, git, "ignore")
	writeGit(t, root, "node_modules/package/index.js", "dependency")
	clone := func(ctx context.Context, from, to string) bool {
		if filepath.Base(from) == "node_modules" {
			_ = os.MkdirAll(to, 0755)
			_ = os.WriteFile(filepath.Join(to, "partial"), []byte("partial"), 0600)
			return false
		}
		return fakeClone(ctx, from, to)
	}
	factory := newFactory(t, FactoryOptions{Path: root, DataDir: t.TempDir(), Clone: clone})
	workspace, err := factory.Open(t.Context(), OpenRequest{ThreadID: "failed_seed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace.directory, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("partial dependency clone retained", err)
	}
}
func TestWorktreePathCannotAliasAnotherThread(t *testing.T) {
	t.Parallel()
	root, _ := initGit(t)
	factory := newFactory(t, FactoryOptions{Path: root, DataDir: t.TempDir(), DisableClone: true})
	workspace, err := factory.Open(t.Context(), OpenRequest{ThreadID: "kept"})
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(workspace.directory), "alias")
	if err := os.Symlink(workspace.directory, alias); err != nil {
		t.Skip("symlink privileges unavailable")
	}
	defer os.Remove(alias)
	if _, err := factory.Remove(t.Context(), "alias", RemoveOptions{Discard: true}); err == nil {
		t.Fatal("alias deleted another thread")
	}
	if _, err := os.Stat(workspace.directory); err != nil {
		t.Fatal("owned thread removed", err)
	}
}
func TestCloneUnavailableDoesNotLeaveProbeDirectories(t *testing.T) {
	t.Parallel()
	root, _ := initGit(t)
	data := t.TempDir()
	factory := newFactory(t, FactoryOptions{Path: root, DataDir: data, Clone: func(context.Context, string, string) bool { return false }})
	shared, err := factory.SharesStorage(t.Context())
	if err != nil || shared {
		t.Fatal(shared, err)
	}
	for _, root := range factory.roots {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("unused clone directory retained", root, err)
		}
	}
	entries, err := os.ReadDir(factory.common)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "ariadne-clone-probe-") {
			t.Fatal("clone probe retained")
		}
	}
}
func TestParseDiffPreservesTabsAndBinaryCounts(t *testing.T) {
	files := ParseDiff("R100\x00old\tname\x00new\tname\x00M\x00binary\x00", "1\t2\t\x00old\tname\x00new\tname\x00-\t-\tbinary\x00")
	if len(files) != 2 || files[0].Path != "new\tname" || files[0].PreviousPath == nil || *files[0].PreviousPath != "old\tname" || files[0].Added == nil || *files[0].Added != 1 || files[1].Added != nil {
		t.Fatal(files)
	}
	if paths := ParseStatus("R  new\tname\x00old\tname\x00?? untracked\x00"); !reflect.DeepEqual(paths, []string{"new\tname", "untracked"}) {
		t.Fatal(paths)
	}
}

func TestCanceledCloneProbeWaiterAndFactoryClose(t *testing.T) {
	root, _ := initGit(t)
	started := make(chan struct{})
	var once sync.Once
	clone := func(ctx context.Context, from, to string) bool {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return false
	}
	factory := newFactory(t, FactoryOptions{Path: root, DataDir: t.TempDir(), Clone: clone})
	done := make(chan error, 1)
	go func() { _, err := factory.SharesStorage(t.Context()); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not start")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	second := make(chan error, 1)
	go func() { _, err := factory.SharesStorage(ctx); second <- err }()
	select {
	case err := <-second:
		if err == nil {
			t.Fatal("canceled waiter succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter blocked behind probe")
	}
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("factory close did not cancel probe")
		}
	case <-time.After(time.Second):
		t.Fatal("factory close left operation running")
	}
}
