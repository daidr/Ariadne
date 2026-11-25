package workspaces

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

type countedReader struct {
	io.ReadCloser
	once   sync.Once
	closed atomic.Int32
}

func (reader *countedReader) Close() error {
	var err error
	reader.once.Do(func() { reader.closed.Add(1); err = reader.ReadCloser.Close() })
	return err
}
func TestRepositoryAndTrackerProxiesDoNotWriteAfterConfigurationChanges(t *testing.T) {
	t.Parallel()
	repo, project, _ := testRepository(t)
	registry := sources.New()
	var opened, closed, writes atomic.Int32
	provider := testProvider(&opened, &closed)
	connect := provider.Connect
	provider.Info.Provides = []string{"workspace", "repo", "tracker"}
	provider.Connect = func(ctx context.Context, settings map[string]any) (sdk.Capabilities, error) {
		caps, err := connect(ctx, settings)
		if err != nil {
			return caps, err
		}
		caps.Repo = &sdk.RepoCapability{Remote: "owner/repository", WebURL: "https://example.test/owner/repository", Push: func(context.Context, string) (string, error) { writes.Add(1); return "sha", nil }, DefaultBranch: func(context.Context) (string, error) { return "main", nil }}
		caps.Tracker = &sdk.TrackerCapability{Name: "issues", WebURL: "https://example.test/issues", Comment: func(context.Context, int, string) (sdk.IssueComment, error) {
			writes.Add(1)
			return sdk.IssueComment{ID: "1"}, nil
		}}
		return caps, nil
	}
	registry.Register(provider)
	var mu sync.Mutex
	token := "one"
	manager, err := New(Options{Repository: repo, Sources: registry, Secrets: func(string) map[string]any { mu.Lock(); defer mu.Unlock(); return map[string]any{"token": token} }})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	git, err := manager.Repo(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := manager.Tracker(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := git.DefaultBranch(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	token = "two"
	mu.Unlock()
	if _, err := git.Push(t.Context(), "feature"); err == nil {
		t.Fatal("stale repository accepted push")
	}
	if _, err := tracker.Comment(t.Context(), 1, "body"); err == nil {
		t.Fatal("stale tracker accepted comment")
	}
	if writes.Load() != 0 {
		t.Fatal("configuration changed after write started")
	}
	current, err := manager.Repo(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.Push(t.Context(), "feature"); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatal("current repository could not write")
	}
}
func TestGitBundleLeasePinsConnectionAndClosesOnManagerShutdown(t *testing.T) {
	t.Parallel()
	repo, project, _ := testRepository(t)
	registry := sources.New()
	var opened, closed atomic.Int32
	reader, writer := io.Pipe()
	defer writer.Close()
	body := &countedReader{ReadCloser: reader}
	provider := testProvider(&opened, &closed)
	connect := provider.Connect
	provider.Info.Provides = []string{"workspace", "repo"}
	provider.Connect = func(ctx context.Context, settings map[string]any) (sdk.Capabilities, error) {
		caps, err := connect(ctx, settings)
		caps.Repo = &sdk.RepoCapability{Objects: &sdk.GitObjects{Bundle: func(context.Context, []string, []string) (io.ReadCloser, error) { return body, nil }}}
		return caps, err
	}
	registry.Register(provider)
	manager, err := New(Options{Repository: repo, Sources: registry, CacheTTL: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	git, err := manager.Repo(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := git.Objects.Bundle(t.Context(), []string{"tip"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	time.Sleep(60 * time.Millisecond)
	if closed.Load() != 0 {
		t.Fatal("idle eviction closed a streaming bundle")
	}
	closeManager(t, manager)
	if body.closed.Load() != 1 || closed.Load() != 1 {
		t.Fatal("shutdown did not release stream and source", body.closed.Load(), closed.Load())
	}
}
func TestGitReceiveClosesInputOnPreflightFailure(t *testing.T) {
	t.Parallel()
	repo, project, _ := testRepository(t)
	registry := sources.New()
	var opened, closed atomic.Int32
	provider := testProvider(&opened, &closed)
	connect := provider.Connect
	provider.Info.Provides = []string{"workspace", "repo"}
	provider.Connect = func(ctx context.Context, settings map[string]any) (sdk.Capabilities, error) {
		caps, err := connect(ctx, settings)
		caps.Repo = &sdk.RepoCapability{Objects: &sdk.GitObjects{Receive: func(_ context.Context, input io.ReadCloser, _ map[string]*string) error {
			if input != nil {
				input.Close()
			}
			return nil
		}}}
		return caps, err
	}
	registry.Register(provider)
	manager, err := New(Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	git, err := manager.Repo(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	writer.Close()
	input := &countedReader{ReadCloser: reader}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := git.Objects.Receive(ctx, input, nil); err == nil {
		t.Fatal("cancelled receive succeeded")
	}
	if input.closed.Load() != 1 {
		t.Fatal("failed preflight retained input")
	}
}
