package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/sdk"
)

func TestTokenPriorityAndCLIIsolation(t *testing.T) {
	var runs atomic.Int32
	run := func(ctx context.Context, args []string, options process.RunOptions) sdk.ExecResult {
		runs.Add(1)
		if strings.Join(args, " ") != "gh auth token" || options.Timeout != 10*time.Second || !options.ForThread || options.Directory == "" {
			t.Errorf("unexpected CLI invocation: %v %+v", args, options)
		}
		return sdk.ExecResult{Stdout: " cli-token\n"}
	}
	for _, test := range []struct {
		configured string
		env        map[string]string
		want       string
	}{{" explicit ", map[string]string{"GH_TOKEN": "env"}, "explicit"}, {"", map[string]string{"GH_TOKEN": " first ", "GITHUB_TOKEN": "second"}, "first"}, {"", map[string]string{"GITHUB_TOKEN": " second "}, "second"}, {"", map[string]string{}, "cli-token"}} {
		token, err := FindToken(t.Context(), test.configured, test.env, run)
		if err != nil || token == nil || *token != test.want {
			t.Fatalf("token resolution failed: %v", err)
		}
	}
	if runs.Load() != 1 {
		t.Fatal("unnecessary CLI resolution", runs.Load())
	}
}
func TestProviderValidatesCachesAndClosesRepository(t *testing.T) {
	var metadata atomic.Int32
	var opened atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("provider lost private token")
		}
		switch r.URL.Path {
		case "/repos/acme/app":
			metadata.Add(1)
			io.WriteString(w, `{"default_branch":"release","private":true}`)
		case "/repos/acme/app/pulls":
			opened.Add(1)
			var input map[string]any
			json.NewDecoder(r.Body).Decode(&input)
			if input["base"] != "release" {
				t.Error("did not use configured default branch")
			}
			json.NewEncoder(w).Encode(pull(7, "open", "feature", "2026-11-01"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := Provider(Options{DataDir: t.TempDir(), APIBase: server.URL, GitBase: server.URL, Client: server.Client(), Env: map[string]string{}})
	caps, err := provider.Connect(t.Context(), map[string]any{"repository": "acme/app", "token": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer caps.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if base, err := caps.Repo.DefaultBranch(t.Context()); err != nil || base != "release" {
				t.Error("default branch unavailable", err)
			}
			if public, err := caps.Repo.IsPublic(t.Context()); err != nil || public {
				t.Error("private classification changed", err)
			}
		}()
	}
	wg.Wait()
	if metadata.Load() != 1 {
		t.Fatal("metadata lookup not shared", metadata.Load())
	}
	if _, err := caps.Repo.OpenPullRequest(t.Context(), sdk.OpenPullRequest{Branch: "feature", Title: "标题"}); err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 1 || caps.Tracker.Name != "acme/app" || caps.Workspace == nil || caps.Repo.Objects == nil {
		t.Fatal("missing capability")
	}
	caps.Close()
	if _, err := caps.Repo.DefaultBranch(t.Context()); err == nil {
		t.Fatal("closed connection retained metadata")
	}
	if _, err := caps.Tracker.List(t.Context(), sdk.IssueQuery{}); err == nil {
		t.Fatal("closed connection still calls remote")
	}
}
func TestPreviewCancellationAndProviderClose(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	links := Links(Options{APIBase: server.URL, Client: server.Client(), Env: map[string]string{"GH_TOKEN": "secret"}})
	address, _ := url.Parse("https://github.com/acme/app/pull/7")
	if match := links.Match(address); match == nil || match.Label != "acme/app#7" {
		t.Fatal("link not recognized")
	}
	finished := make(chan error, 1)
	go func() { _, err := links.Preview(t.Context(), address); finished <- err }()
	<-started
	links.Close()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("closed preview succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("preview retained after close")
	}
	if _, err := links.Preview(t.Context(), address); err == nil {
		t.Fatal("closed provider admitted preview")
	}
}
