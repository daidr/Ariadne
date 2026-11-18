package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/sdk"
)

func TestTokenResolutionKeepsEnvironmentWithinItsHost(t *testing.T) {
	var runs atomic.Int32
	run := func(ctx context.Context, args []string, options process.RunOptions) sdk.ExecResult {
		runs.Add(1)
		if !reflect.DeepEqual(args, []string{"glab", "config", "get", "token", "--host", "other.test:8443"}) {
			t.Error("wrong host lookup", args)
		}
		for _, key := range []string{"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "GL_TOKEN", "OAUTH_TOKEN", "CI_JOB_TOKEN"} {
			if value, ok := options.Env[key]; !ok || value != "" {
				t.Error("CLI may inherit token for another host", key)
			}
		}
		if !options.ForThread || options.Timeout != 10*time.Second {
			t.Error("CLI lifecycle not bounded")
		}
		return sdk.ExecResult{Stdout: " cli-other \n"}
	}
	for _, test := range []struct {
		configured, instance string
		env                  map[string]string
		want                 string
	}{
		{" explicit ", "https://other.test:8443", map[string]string{"GITLAB_TOKEN": "wrong-host"}, "explicit"},
		{"", "https://gitlab.com", map[string]string{"GITLAB_TOKEN": " main "}, "main"},
		{"", "https://other.test:8443/gitlab", map[string]string{"GITLAB_HOST": "https://Other.Test:8443", "GL_TOKEN": "custom-host"}, "custom-host"},
		{"", "https://other.test:8443", map[string]string{"GITLAB_TOKEN": "never-send-this", "OAUTH_TOKEN": "wrong", "CI_JOB_TOKEN": "wrong"}, "cli-other"},
		{"", "https://other.test:8443", map[string]string{"GITLAB_HOST": "other.test", "GITLAB_TOKEN": "wrong-port"}, "cli-other"},
	} {
		token, err := FindToken(t.Context(), test.configured, test.instance, test.env, run)
		if err != nil || token == nil || *token != test.want {
			t.Fatalf("wrong scoped token: %v", err)
		}
	}
	if runs.Load() != 2 {
		t.Fatal("unnecessary CLI lookup", runs.Load())
	}
}
func TestInstancesUpdateDeleteBoundsAndClose(t *testing.T) {
	instances := NewInstances(map[string]string{})
	defer instances.Close()
	address, _ := url.Parse("https://one.test/group/app/-/merge_requests/1")
	if err := instances.Add("https://one.test", "group/app", "old-secret"); err != nil {
		t.Fatal(err)
	}
	if err := instances.Add("https://two.test", "group/app", "other-secret"); err != nil {
		t.Fatal(err)
	}
	if found := instances.Find(address); found == nil || !reflect.DeepEqual(found.Tokens, []string{"old-secret"}) {
		t.Fatal("instance credential isolation failed")
	}
	if err := instances.Add("https://one.test", "group/app", ""); err != nil {
		t.Fatal(err)
	}
	if found := instances.Find(address); found == nil || len(found.Tokens) != 0 {
		t.Fatal("cleared configuration retained old token")
	}
	instances.Remove("https://one.test", "group/app")
	if instances.Find(address) != nil {
		t.Fatal("deleted project retained instance")
	}
	if err := instances.Add("https://one.test", "group/app", strings.Repeat("x", 64*1024+1)); err == nil {
		t.Fatal("unbounded secret")
	}
	for i := 0; i < 126; i++ {
		if err := instances.Add(fmt.Sprintf("https://host%d.test", i), "group/app", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := instances.Add("https://overflow.test", "group/app", ""); err == nil {
		t.Fatal("unbounded instance registry")
	}
	instances.Close()
	instances.mu.RLock()
	retained := len(instances.entries) != 0 || len(instances.order) != 0 || instances.weight != 0 || instances.projects != 0
	instances.mu.RUnlock()
	if retained {
		t.Fatal("closed instances retained tokens")
	}
}
func TestLinkPreviewTriesOnlyTokensOfItsInstanceAndReleasesRequests(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer group-token" {
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"Not found"}`)
			return
		}
		json.NewEncoder(w).Encode(mergeRaw(7, "opened", "feature", "2026-11-01"))
	}))
	defer server.Close()
	instances := NewInstances(map[string]string{})
	defer instances.Close()
	for _, item := range []struct{ host, path, token string }{{server.URL, "group/sub/app", "own-inaccessible"}, {server.URL, "group/other", "group-token"}, {"https://other.test", "group/sub/app", "NEVER-OTHER-HOST"}} {
		if err := instances.Add(item.host, item.path, item.token); err != nil {
			t.Fatal(err)
		}
	}
	links := Links(Options{Instances: instances, Client: server.Client(), Env: map[string]string{}, Run: func(context.Context, []string, process.RunOptions) sdk.ExecResult {
		t.Error("should not need CLI after accessible token")
		return sdk.ExecResult{ExitCode: 1}
	}})
	defer links.Close()
	address, _ := url.Parse(server.URL + "/group/sub/app/-/merge_requests/7")
	if match := links.Match(address); match == nil || match.Label != "group/sub/app!7" {
		t.Fatal("missing link summary")
	}
	preview, err := links.Preview(t.Context(), address)
	if err != nil || preview == nil || preview.Number != 7 {
		t.Fatal("preview failed", err)
	}
	mu.Lock()
	sent := append([]string(nil), requests...)
	mu.Unlock()
	if !reflect.DeepEqual(sent, []string{"Bearer own-inaccessible", "Bearer group-token"}) {
		t.Fatal("wrong token attempts")
	}
	if summary := instances.find(address, false); summary == nil || len(summary.Tokens) != 0 {
		t.Fatal("rendering link copied credentials")
	}
	links.Close()
	if _, err := links.Preview(t.Context(), address); err == nil {
		t.Fatal("closed provider accepted work")
	}
}
func TestProviderDoesNotRegisterFailedCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"message":"Unauthorized"}`)
	}))
	defer server.Close()
	instances := NewInstances(map[string]string{})
	defer instances.Close()
	provider := Provider(Options{DataDir: t.TempDir(), Instances: instances, Client: server.Client(), Env: map[string]string{}})
	if _, err := provider.Connect(t.Context(), map[string]any{"repository": "group/app", "instance": server.URL, "token": "invalid"}); err == nil {
		t.Fatal("failed credentials accepted")
	}
	address, _ := url.Parse(server.URL + "/group/app/-/merge_requests/1")
	if instances.Find(address) != nil {
		t.Fatal("failed validation retained credentials")
	}
}
func TestHostedProviderUsesWindowsSafeInstanceDirectoriesAndDefaultBranch(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin", "group", "sub", "app.git")
	if err := os.MkdirAll(origin, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(directory string, args ...string) string {
		t.Helper()
		// Use the same isolated Git configuration as the hosted implementation;
		// a developer's global signing or hooks must not run in this fixture.
		result := local.NewGit(directory, "file")(t.Context(), args, local.GitOptions{Timeout: 10 * time.Second})
		if result.ExitCode != 0 {
			t.Fatalf("git failed %v: %s", args, result.Stderr)
		}
		return strings.TrimSpace(result.Stdout)
	}
	git(origin, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "base.txt"), []byte("source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(origin, "add", "base.txt")
	git(origin, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "initial")
	// The receiver need not be checked out: pushes to a checked-out branch are refused by Git.
	git(origin, "config", "receive.denyCurrentBranch", "ignore")
	var metadata atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer explicit-token" {
			t.Error("missing REST token")
		}
		if r.URL.Path == "/api/v4/projects/group/sub/app" {
			metadata.Add(1)
			io.WriteString(w, `{"id":15,"default_branch":"main","web_url":"https://example.test/group/sub/app","path_with_namespace":"group/sub/app","visibility":"internal"}`)
			return
		}
		if r.URL.Path == "/api/v4/projects/group/sub/app/merge_requests" {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["target_branch"] != "main" || body["title"] != "Draft: 标题" {
				t.Error("wrong default branch or draft conversion", body)
			}
			json.NewEncoder(w).Encode(mergeRaw(7, "opened", "feature", "2026-11-01"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	remoteURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(root, "origin"))}
	if !strings.HasPrefix(remoteURL.Path, "/") {
		remoteURL.Path = "/" + remoteURL.Path
	}
	provider := Provider(Options{DataDir: filepath.Join(root, "data"), GitBase: remoteURL.String(), Client: server.Client(), Env: map[string]string{}, DisableClone: true})
	caps, err := provider.Connect(t.Context(), map[string]any{"repository": "group/sub/app", "instance": server.URL, "token": "explicit-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer caps.Close()
	if public, err := caps.Repo.IsPublic(t.Context()); err != nil || !public {
		t.Fatal("internal repository classification changed", err)
	}
	workspace, err := caps.Workspace.Open(t.Context(), sdk.WorkspaceRequest{ThreadID: "thread-123456789012"})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	if text, err := workspace.ReadFile(t.Context(), "base.txt"); err != nil || text != "source\n" {
		t.Fatal("hosted checkout missing files", err)
	}
	if _, err := caps.Repo.OpenPullRequest(t.Context(), sdk.OpenPullRequest{Branch: "feature", Title: "标题", Draft: true}); err != nil {
		t.Fatal(err)
	}
	if metadata.Load() != 1 {
		t.Fatal("provider did not share project metadata", metadata.Load())
	}
	host, _ := url.Parse(server.URL)
	component := InstanceDirs(server.URL)[0]
	if strings.ContainsAny(component, ":[]") || !strings.Contains(component, "%3A") || !strings.Contains(host.Host, ":") {
		t.Fatal("port namespace not Windows safe", component)
	}
	if info := workspace.Info(); info.Branch == nil {
		t.Fatal("missing thread branch")
	}
	caps.Close()
	if _, err := caps.Repo.DefaultBranch(t.Context()); err == nil {
		t.Fatal("closed connection retained project")
	}
}
func TestLinkCloseCancelsCLILookup(t *testing.T) {
	started := make(chan struct{})
	instances := NewInstances(map[string]string{})
	defer instances.Close()
	links := Links(Options{Instances: instances, Env: map[string]string{}, Run: func(ctx context.Context, _ []string, _ process.RunOptions) sdk.ExecResult {
		close(started)
		<-ctx.Done()
		return sdk.ExecResult{ExitCode: 130}
	}})
	address, _ := url.Parse("https://gitlab.com/group/app/-/merge_requests/1")
	finished := make(chan error, 1)
	go func() { _, err := links.Preview(t.Context(), address); finished <- err }()
	<-started
	links.Close()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled lookup succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("CLI lookup retained after provider close")
	}
}
func TestLargePlainLogStreamsWithoutRetainingFullText(t *testing.T) {
	text := "section_start:1:build\r\x1b[0K" + strings.Repeat("\x1b[32mline\x1b[0m\r", 100_000) + "section_end:2:build\r\x1b[0K\r\n"
	streamed, err := io.ReadAll(plainStream(strings.NewReader(text)))
	if err != nil || string(streamed) != PlainLog(text) {
		t.Fatal("large streamed log differs", err)
	}
	if _, err := io.ReadAll(plainStream(strings.NewReader("section_start:1:" + strings.Repeat("x", 64*1024+1)))); err == nil {
		t.Fatal("unbounded section marker")
	}
}
