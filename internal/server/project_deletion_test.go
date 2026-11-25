package server

import (
	"context"
	"iter"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

func TestConcurrentProjectDeletionSharesCleanupAndSurvivesDisconnectedWaiter(t *testing.T) {
	t.Parallel()
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
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
	srv := httptest.NewServer(api)
	defer srv.Close()

	unauth := ariadnev1connect.NewProjectServiceClient(srv.Client(), srv.URL+rpcPrefix)
	if _, err := unauth.DeleteProject(t.Context(), connect.NewRequest(&ariadnev1.DeleteProjectRequest{ProjectId: "prj_a"})); connect.CodeOf(err) != connect.CodeUnauthenticated || removed.Load() != 0 {
		t.Fatalf("unauthorized deletion reached source: err=%v removed=%d", err, removed.Load())
	}

	client := ariadnev1connect.NewProjectServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("secret")),
	)
	firstCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	invoke := func(ctx context.Context) <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := client.DeleteProject(ctx, connect.NewRequest(&ariadnev1.DeleteProjectRequest{ProjectId: "prj_a"}))
			done <- err
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
	case err := <-second:
		if err != nil {
			t.Fatalf("shared deletion failed: %v", err)
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
	t.Parallel()
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
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

	srv := httptest.NewServer(api)
	defer srv.Close()
	client := ariadnev1connect.NewProjectServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("secret")),
	)
	if _, err := client.DeleteProject(t.Context(), connect.NewRequest(&ariadnev1.DeleteProjectRequest{ProjectId: project.ID})); err != nil {
		t.Fatalf("DeleteProject failed: %v", err)
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
