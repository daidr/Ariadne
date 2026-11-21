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
func TestHostedCloneBranchWorkAndSafePush(t *testing.T) {
	t.Parallel()
	remote, remoteGit := initGit(t)
	storage := t.TempDir()
	data, err := HostedDataDir(filepath.Join(t.TempDir(), "github"), storage)
	if err != nil {
		t.Fatal(err)
	}
	host := newHostedTest(t, remote, data, nil)
	view, err := host.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if data, err := view.ReadBytes(t.Context(), "file.txt", 1024); err != nil || string(data) != "base\n" {
		t.Fatal(string(data), err)
	}
	workspace, err := host.Open(t.Context(), sdk.WorkspaceRequest{ThreadID: "hosted_thread"})
	if err != nil {
		t.Fatal(err)
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
	if err := workspace.WriteFile(t.Context(), "file.txt", "thread work\n"); err != nil {
		t.Fatal(err)
	}
	git := NewGit(info.Label, "file")
	gitOK(t, git, "config", "user.name", "Test")
	gitOK(t, git, "config", "user.email", "test@example.invalid")
	head := gitCommit(t, git, "thread work")
	gitOK(t, git, "config", "remote.origin.url", "file:///must-not-be-used")
	sha, err := host.Push(t.Context(), *info.Branch)
	if err != nil || sha != head {
		t.Fatal(sha, err)
	}
	if actual := gitOK(t, remoteGit, "rev-parse", "refs/heads/"+*info.Branch); actual != head {
		t.Fatal(actual)
	}
	if result, err := workspace.ExecFile(t.Context(), []string{"git", "push", "origin", *info.Branch}, sdk.ExecOptions{Timeout: 10 * time.Second}); err != nil || result.ExitCode == 0 {
		t.Fatal("workspace bypassed hosted push tool", result, err)
	}
	tree := gitOK(t, remoteGit, "rev-parse", head+"^{tree}")
	moved := gitOK(t, remoteGit, "commit-tree", tree, "-p", head, "-m", "remote work")
	gitOK(t, remoteGit, "update-ref", "refs/heads/"+*info.Branch, moved)
	if _, err := host.Push(t.Context(), *info.Branch); err == nil || !strings.Contains(err.Error(), "Never force-push") {
		t.Fatal(err)
	}
	if actual := gitOK(t, remoteGit, "rev-parse", "refs/heads/"+*info.Branch); actual != moved {
		t.Fatal("remote history overwritten")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := view.ReadBytes(t.Context(), "file.txt", 1024); err == nil {
		t.Fatal("closed hosted source still starts readers")
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
func TestHostedSharedCloneConcurrentProjectsAndClonedCheckout(t *testing.T) {
	t.Parallel()
	remote, _ := initGit(t)
	data := t.TempDir()
	one, two := newHostedTest(t, remote, data, fakeClone), newHostedTest(t, remote, data, fakeClone)
	var jobs sync.WaitGroup
	failures := make(chan error, 2)
	opened := make(chan sdk.Workspace, 2)
	for index, host := range []*Hosted{one, two} {
		jobs.Go(func() {
			workspace, err := host.Open(t.Context(), sdk.WorkspaceRequest{ThreadID: fmt.Sprintf("thread_%d", index)})
			if err != nil {
				failures <- err
			} else {
				opened <- workspace
			}
		})
	}
	jobs.Wait()
	close(failures)
	close(opened)
	for err := range failures {
		t.Error(err)
	}
	for workspace := range opened {
		if data, err := workspace.ReadFile(t.Context(), "file.txt"); err != nil || data != "base\n" {
			t.Error(data, err)
		}
	}
	if t.Failed() {
		return
	}
	hostedOperations.Lock()
	retained := false
	for key := range hostedOperations.entries {
		if strings.EqualFold(key, one.bare) {
			retained = true
		}
	}
	hostedOperations.Unlock()
	if retained {
		t.Fatal("shared clone operation locks retained")
	}
	if err := one.Close(); err != nil {
		t.Fatal(err)
	}
	view, err := two.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if data, err := view.ReadBytes(t.Context(), "file.txt", 1024); err != nil || string(data) != "base\n" {
		t.Fatal(string(data), err)
	}
	branches := two.Capability().Branches
	if branches.Merge != nil || branches.UndoMerge != nil {
		t.Fatal("hosted branches expose local merging")
	}
	removed, err := branches.Remove(t.Context(), "thread_1", sdk.WorktreeRemoveOptions{Spare: true})
	if err != nil || !removed {
		t.Fatal(removed, err)
	}
	if sha := commitSHA(t.Context(), two.owner.git, "refs/heads/"+BranchFor("thread_1"), ""); sha == "" {
		t.Fatal("removed thread's branch lost")
	}
}
func TestHostedGitEnvBindsCredentialsToURLAndDisablesHelpers(t *testing.T) {
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

func TestHostedPushReportsTheCommitItActuallySends(t *testing.T) {
	remote, remoteGit := initGit(t)
	host := newHostedTest(t, remote, t.TempDir(), nil)
	workspace, err := host.Open(t.Context(), sdk.WorkspaceRequest{ThreadID: "push_race"})
	if err != nil {
		t.Fatal(err)
	}
	branch := *workspace.Info().Branch
	ref := "refs/heads/" + branch
	head := gitOK(t, host.owner.git, "rev-parse", ref)
	tree := gitOK(t, host.owner.git, "rev-parse", head+"^{tree}")
	next := gitOK(t, host.owner.git, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit-tree", tree, "-p", head, "-m", "arrived while pushing")
	host.options.Token = func(context.Context) (*string, error) {
		gitOK(t, host.owner.git, "update-ref", ref, next)
		return nil, nil
	}
	pushed, err := host.Push(t.Context(), branch)
	if err != nil || pushed != head {
		t.Fatal(pushed, err)
	}
	if remoteHead := gitOK(t, remoteGit, "rev-parse", ref); remoteHead != pushed {
		t.Fatal("reported a different commit than pushed", remoteHead, pushed)
	}
	if localHead := gitOK(t, host.owner.git, "rev-parse", ref); localHead != next {
		t.Fatal("concurrent local work lost", localHead, next)
	}
}
