package server

import (
	"context"
	"iter"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

func TestConcurrentProjectDeletionSharesCleanupAndSurvivesDisconnectedWaiter(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "delete.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), workspaceHTTPSeed()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var removed, forgot atomic.Int32
	registry := sources.New()
	registry.Register(sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "fixture", Provides: []string{"workspace"}}, Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
		return sdk.Capabilities{Workspace: &sdk.WorkspaceCapability{Branches: &sdk.BranchCapability{Remove: func(ctx context.Context, id string, options sdk.WorktreeRemoveOptions) (bool, error) {
			if removed.Add(1) == 1 {
				close(entered)
			}
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-release:
				return options.Discard, nil
			}
		}}}}, nil
	}})
	manager, err := workspaces.New(workspaces.Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	api, err := New(Options{Repository: repo, Token: "secret", Workspaces: manager, OnProjectDeleted: func(string) { forgot.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	unauthorized := httptest.NewRecorder()
	api.ServeHTTP(unauthorized, httptest.NewRequest("DELETE", "/api/projects/prj_a", nil))
	if unauthorized.Code != 401 || removed.Load() != 0 {
		t.Fatal("unauthorized deletion reached source")
	}
	firstCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	invoke := func(ctx context.Context) <-chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			request := httptest.NewRequest("DELETE", "/api/projects/prj_a", nil).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer secret")
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			done <- response
		}()
		return done
	}
	first := invoke(firstCtx)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("deletion did not begin")
	}
	second := invoke(t.Context())
	deadline := time.Now().Add(time.Second)
	for {
		api.deletionMu.Lock()
		waiters := api.deleteWaiters
		api.deletionMu.Unlock()
		if waiters == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second deletion did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-first
	if _, err := repo.GetProject(t.Context(), "prj_a"); err != nil {
		t.Fatal("project deleted before cleanup finished", err)
	}
	close(release)
	select {
	case response := <-second:
		if response.Code != 204 {
			t.Fatal(response.Code, response.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shared deletion did not complete")
	}
	if removed.Load() != 1 || forgot.Load() != 1 {
		t.Fatal("duplicate cleanup", removed.Load(), forgot.Load())
	}
	if _, err := repo.GetProject(t.Context(), "prj_a"); err == nil {
		t.Fatal("project retained")
	}
	api.deletionMu.Lock()
	retained := len(api.deletions)
	api.deletionMu.Unlock()
	if retained != 0 {
		t.Fatal("deletion metadata retained")
	}
}

func TestProjectDeletionCallsModelProviderForget(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "delete-forget.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	name := "Forget Project"
	project, err := repo.CreateProject(t.Context(), &name, core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(t.Context(), project.ID, core.NewThread{Title: "Thread 1"})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var forgotten []string
	modelReg := models.New()
	remove, err := modelReg.Register(sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{Kind: "stateful-fixture", Program: true},
		Stream: func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] {
			return func(func(sdk.Part) bool) {}
		},
		Forget: func(_ context.Context, conversation string) error {
			mu.Lock()
			forgotten = append(forgotten, conversation)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer remove()

	api, err := New(Options{Repository: repo, Token: "secret", Models: modelReg})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	req := httptest.NewRequest("DELETE", "/api/projects/"+project.ID, nil)
	req.Header.Set("Authorization", "Bearer secret")
	res := httptest.NewRecorder()
	api.ServeHTTP(res, req)
	if res.Code != 204 {
		t.Fatalf("expected 204, got %d: %s", res.Code, res.Body.String())
	}

	mu.Lock()
	got := slices.Clone(forgotten)
	mu.Unlock()
	slices.Sort(got)
	want := []string{project.CoordinatorID, thread.ID}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("expected Forget for %v, got %v", want, got)
	}
}
