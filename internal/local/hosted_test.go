package local

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/fsutil"
	"github.com/daidr/Ariadne/sdk"
)

func fileURL(path string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}
func newHostedTest(t *testing.T, remote, data string, clone Cloner) *Hosted {
	t.Helper()
	host, err := NewHosted(HostedOptions{DataDir: data, Path: "owner/repository", RemoteURL: fileURL(remote), User: "server", Token: func(context.Context) (*string, error) { return new("server-test-token"), nil }, DefaultBranch: func(context.Context) (string, error) { return "main", nil }, Clone: clone, DisableClone: clone == nil})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	return host
}
func TestHostedCloneSharedWorkAndSafePush(t *testing.T) {
	if testing.Short() {
		t.Skip("hosted bare-clone matrix covered by full suite")
	}
	t.Parallel()
	remote, remoteGit := initGit(t)
	storage := t.TempDir()
	data, err := HostedDataDir(filepath.Join(t.TempDir(), "github"), storage)
	if err != nil {
		t.Fatal(err)
	}
	host := newHostedTest(t, remote, data, fakeClone)
	peer := newHostedTest(t, remote, data, fakeClone)

	// Concurrent Open across two projects sharing the same bare clone coalesces cloning safely.
	var jobs sync.WaitGroup
	failures := make(chan error, 2)
	var workspace, peerWS sdk.Workspace
	jobs.Go(func() {
		ws, err := host.Open(t.Context(), sdk.WorkspaceRequest{ThreadID: "hosted_thread"})
		if err != nil {
			failures <- err
		} else {
			workspace = ws
		}
	})
	jobs.Go(func() {
		ws, err := peer.Open(t.Context(), sdk.WorkspaceRequest{ThreadID: "peer_thread"})
		if err != nil {
			failures <- err
		} else {
			peerWS = ws
		}
	})
	jobs.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	hostedOperations.Lock()
	retained := false
	for key := range hostedOperations.entries {
		if strings.EqualFold(key, host.bare) {
			retained = true
		}
	}
	hostedOperations.Unlock()
	if retained {
		t.Fatal("shared clone operation locks retained")
	}
	if got, err := peerWS.ReadFile(t.Context(), "file.txt"); err != nil || got != "base\n" {
		t.Fatal(got, err)
	}
	view, err := host.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := view.ReadBytes(t.Context(), "file.txt", 1024); err != nil || string(raw) != "base\n" {
		t.Fatal(string(raw), err)
	}
	info := workspace.Info()
	if info.Branch == nil || info.Base == nil || *info.Base != "main" || !info.Local {
		t.Fatal(info)
	}
	expectedStorage, err := fsutil.CanonicalPath(storage)
	if err != nil {
		t.Fatal(err)
	}
	if host.bare != filepath.Join(expectedStorage, "ariadne", "github", "repos", "owner", "repository.git") ||
		filepath.Dir(info.Label) != filepath.Join(expectedStorage, "ariadne", "github", "worktrees", "owner", "repository") {
		t.Fatal("clone and worktree were not written beneath the selected directory", host.bare, info.Label)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result, err := workspace.ExecFile(t.Context(), []string{program, "-test.run=^TestHostedEnvironmentChild$", "--", "--hosted-env"}, sdk.ExecOptions{})
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != "[]" {
		t.Fatal(result, err)
	}
	config, err := os.ReadFile(filepath.Join(host.bare, "config"))
	if err != nil || strings.Contains(string(config), "server-test-token") {
		t.Fatal("credential written into git config", err)
	}
	branches := host.Capability().Branches
	if branches.Merge != nil || branches.UndoMerge != nil {
		t.Fatal("hosted branches expose local merging")
	}
	if branches.Snapshot == nil || branches.Diff == nil {
		t.Fatal("hosted branches missing snapshot or diff")
	}
	if err := workspace.WriteFile(t.Context(), "file.txt", "base\nthread work\n"); err != nil {
		t.Fatal(err)
	}
	afterSnap, err := branches.Snapshot(t.Context(), "hosted_thread")
	if err != nil || afterSnap == nil || *afterSnap == "" {
		t.Fatalf("after snapshot: %v, err=%v", afterSnap, err)
	}
	diffFiles, err := branches.Diff(t.Context(), "refs/remotes/origin/main", *afterSnap)
	if err != nil || len(diffFiles) != 1 || diffFiles[0].Path != "file.txt" || diffFiles[0].Added == nil || *diffFiles[0].Added != 1 {
		t.Fatalf("unexpected diff: %+v, err=%v", diffFiles, err)
	}
	git := NewGit(info.Label, "file")
	gitCommitTracked(t, git, "thread work")
	head := readHeadSHA(t, info.Label)
	appendGitConfig(t, host.bare, "\n[remote \"origin\"]\n\turl = file:///must-not-be-used\n")
	// Verify Push reports the exact commit it sends even if another commit arrives while fetching the token.
	branchRef := "refs/heads/" + *info.Branch
	next := gitOK(t, host.owner.git, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit-tree", head+"^{tree}", "-p", head, "-m", "arrived while pushing")
	origToken := host.options.Token
	host.options.Token = func(ctx context.Context) (*string, error) {
		writeGitRef(t, host.bare, branchRef, next)
		return origToken(ctx)
	}
	sha, err := host.Push(t.Context(), *info.Branch)
	host.options.Token = origToken
	if err != nil || sha != head {
		t.Fatal(sha, err)
	}
	if actual := readRefSHA(t, filepath.Join(remote, ".git"), branchRef); actual != head {
		t.Fatal(actual)
	}
	if localHead := readRefSHA(t, host.bare, branchRef); localHead != next {
		t.Fatal("concurrent local work lost", localHead, next)
	}
	if result, err := workspace.ExecFile(t.Context(), []string{gitExecutable(nil), "push", "origin", *info.Branch}, sdk.ExecOptions{Timeout: 10 * time.Second}); err != nil || result.ExitCode == 0 {
		t.Fatal("workspace bypassed hosted push tool", result, err)
	}
	moved := gitOK(t, remoteGit, "commit-tree", head+"^{tree}", "-p", head, "-m", "remote work")
	writeGitRef(t, filepath.Join(remote, ".git"), branchRef, moved)
	if _, err := host.Push(t.Context(), *info.Branch); err == nil || !strings.Contains(err.Error(), "Never force-push") {
		t.Fatal(err)
	}
	if actual := readRefSHA(t, filepath.Join(remote, ".git"), branchRef); actual != moved {
		t.Fatal("remote history overwritten")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := view.ReadBytes(t.Context(), "file.txt", 1024); err == nil {
		t.Fatal("closed hosted source still starts readers")
	}
	// Closing one host leaves the peer sharing the same bare clone usable and able to remove its worktree.
	peerView, err := peer.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := peerView.ReadBytes(t.Context(), "file.txt", 1024); err != nil || string(raw) != "base\n" {
		t.Fatal(string(raw), err)
	}
	removed, err := peer.Capability().Branches.Remove(t.Context(), "peer_thread", sdk.WorktreeRemoveOptions{Spare: true})
	if err != nil || !removed {
		t.Fatal(removed, err)
	}
	if sha := readRefSHA(t, peer.bare, "refs/heads/"+BranchFor("peer_thread")); sha == "" {
		t.Fatal("removed thread's branch lost")
	}
}
func TestHostedEnvironmentChild(t *testing.T) {
	for _, arg := range os.Args {
		if arg == "--hosted-env" {
			names := []string{}
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if strings.HasPrefix(key, "GIT_CONFIG_") || strings.HasPrefix(key, "ARIADNE_") || key == "GH_TOKEN" || key == "GITHUB_TOKEN" {
					names = append(names, key)
				}
			}
			data, _ := json.Marshal(names)
			fmt.Print(string(data))
			os.Exit(0)
		}
	}
}
func TestHostedGitEnvBindsCredentialsToURLAndDisablesHelpers(t *testing.T) {
	t.Parallel()
	remote := "https://git.example.test/owner/repository.git"
	env, err := HostedGitEnv(remote, &GitCredential{User: "server", Token: "private-token"})
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]string{}
	for i := 0; i < 20; i++ {
		if key := env[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)]; key != "" {
			config[key] = env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)]
		}
	}
	if config["http."+remote+".extraHeader"] != "Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte("server:private-token")) || config["http.followRedirects"] != "false" || config["credential.helper"] != "" || config["fetch.recurseSubmodules"] != "false" {
		t.Fatal("incorrect hardened git configuration")
	}
	if _, err := HostedGitEnv("https://user:password@git.example.test/repo", nil); err == nil {
		t.Fatal("credentials in remote URL accepted")
	}
}
