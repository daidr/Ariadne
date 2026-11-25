package workspaces

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

func testRepository(t *testing.T) (*core.Repository, core.ProjectView, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "source")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	name := "project"
	project, err := repo.CreateProject(t.Context(), &name, core.ProjectStart{Source: map[string]any{"kind": "test", "settings": map[string]any{"path": path}}})
	if err != nil {
		t.Fatal(err)
	}
	return repo, project, path
}
func testProvider(opened, closed *atomic.Int32) sdk.ContextProvider {
	return sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "test", Title: sdk.LocalizedText{"zh-CN": "测试", "en": "Test"}, Provides: []string{"workspace"}, Settings: sdk.SettingsSchema{Type: "object", FieldOrder: []string{"path", "token"}, Properties: map[string]sdk.SettingField{"path": {Type: "string"}, "token": {Type: "string", Secret: true}}}}, Connect: func(ctx context.Context, settings map[string]any) (sdk.Capabilities, error) {
		path, _ := settings["path"].(string)
		workspace, err := local.NewWorkspace(path, local.WorkspaceOptions{ID: "source", HideGit: true})
		if err != nil {
			return sdk.Capabilities{}, err
		}
		opened.Add(1)
		var once sync.Once
		return sdk.Capabilities{Workspace: &sdk.WorkspaceCapability{View: func(ctx context.Context) (sdk.SourceView, error) { return workspace, ctx.Err() }}, Close: func() error { var err error; once.Do(func() { err = workspace.Close(); closed.Add(1) }); return err }}, nil
	}}
}
func closeManager(t *testing.T, manager *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Error(err)
	}
}
func TestSourceViewsReuseConnectionsAndCloseOnConfigurationOrProviderChange(t *testing.T) {
	t.Parallel()
	repo, project, path := testRepository(t)
	registry := sources.New()
	var opened, closed atomic.Int32
	remove, err := registry.Register(testProvider(&opened, &closed))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	view, err := manager.View(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, mutable := view.(sdk.Workspace); mutable {
		t.Fatal("source view exposed workspace execution")
	}
	for i := 0; i < 3; i++ {
		data, err := view.ReadBytes(t.Context(), "file.txt", 100)
		if err != nil || string(data) != "hello" {
			t.Fatal("view failed", err)
		}
	}
	if opened.Load() != 1 {
		t.Fatal("source not reused", opened.Load())
	}
	if text, err := manager.Describe(t.Context(), project.ID, "en"); err != nil || text == nil || *text != "Test "+path {
		t.Fatal("source description changed", err)
	}
	if !manager.Available(t.Context(), project.ID) || manager.HasRepo(t.Context(), project.ID) || manager.HasTracker(t.Context(), project.ID) {
		t.Fatal("provider capabilities changed")
	}
	remove()
	registry.Register(testProvider(&opened, &closed))
	if _, err := view.ReadBytes(t.Context(), "file.txt", 100); err == nil {
		t.Fatal("old view used replaced provider")
	}
	refreshed, err := manager.View(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refreshed.List(t.Context(), "."); err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 2 {
		t.Fatal("provider replacement not reconnected")
	}
	if _, err := repo.SetProjectSource(t.Context(), project.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := refreshed.List(t.Context(), "."); err == nil {
		t.Fatal("cleared source still readable")
	}
	manager.InvalidateProject(t.Context(), project.ID)
	closeManager(t, manager)
	if closed.Load() != opened.Load() {
		t.Fatal("source connections retained", opened.Load(), closed.Load())
	}
}
func TestCoordinatorViewabilityDoesNotWaitForSlowSourceAndCloseCancelsLoad(t *testing.T) {
	repo, project, _ := testRepository(t)
	registry := sources.New()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	provider := sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "test", Provides: []string{"workspace"}}, Connect: func(ctx context.Context, _ map[string]any) (sdk.Capabilities, error) {
		close(started)
		defer close(cancelled)
		<-ctx.Done()
		return sdk.Capabilities{}, ctx.Err()
	}}
	registry.Register(provider)
	manager, err := New(Options{Repository: repo, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	viewDone := make(chan struct{})
	go func() {
		defer close(viewDone)
		_, _ = manager.View(t.Context(), project.ID)
	}()
	<-started
	start := time.Now()
	if !manager.Viewable(t.Context(), project.ID, 30*time.Millisecond) || time.Since(start) > 2*time.Second {
		t.Fatal("coordinator waited for source connection")
	}
	closeManager(t, manager)
	<-viewDone
	select {
	case <-cancelled:
	default:
		t.Fatal("source lookup retained after close")
	}
}
func TestReferenceViewsRevalidateSecretsAndDeletion(t *testing.T) {
	t.Parallel()
	repo, project, path := testRepository(t)
	registry := sources.New()
	var opened, closed atomic.Int32
	registry.Register(testProvider(&opened, &closed))
	if _, err := repo.AddProjectReference(t.Context(), project.ID, core.Source{Kind: "test", Settings: map[string]any{"path": path}}, "reference"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	secrets := map[string]any{"token": "first"}
	manager, err := New(Options{Repository: repo, Sources: registry, Secrets: func(string) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		copy := map[string]any{}
		for key, value := range secrets {
			copy[key] = value
		}
		return copy
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	view, err := manager.ReferenceView(t.Context(), project.ID, "reference", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := view.List(t.Context(), "."); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	secrets["token"] = "second"
	mu.Unlock()
	if _, err := view.List(t.Context(), "."); err == nil {
		t.Fatal("reference kept revoked token")
	}
	latest, err := manager.ReferenceView(t.Context(), project.ID, "reference", "")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 2 {
		t.Fatal("reference token did not reconnect", opened.Load())
	}
	current, err := repo.GetProject(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	refs := referencesFromView(current.References)
	if len(refs) != 1 {
		t.Fatal("missing reference")
	}
	if _, err := repo.RemoveProjectReference(t.Context(), project.ID, refs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := latest.List(t.Context(), "."); err == nil {
		t.Fatal("deleted reference still readable")
	}
	manager.InvalidateProject(t.Context(), project.ID)
	closeManager(t, manager)
	if closed.Load() != opened.Load() {
		t.Fatal("reference connections retained")
	}
}
func referencesFromView(raw any) []projectReference { return references(raw) }
func makeClosedManagerView(t *testing.T, repo *core.Repository, registry *sources.Registry, project string) sdk.SourceView {
	secret := strings.Repeat("x", 32*1024*1024)
	manager, err := New(Options{Repository: repo, Sources: registry, Secrets: func(string) map[string]any { return map[string]any{"token": secret} }})
	if err != nil {
		t.Fatal(err)
	}
	view, err := manager.View(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	closeManager(t, manager)
	return view
}
func TestClosedViewDoesNotRetainCapturedSourceSecrets(t *testing.T) {
	t.Parallel()
	repo, project, _ := testRepository(t)
	registry := sources.New()
	var opened, closed atomic.Int32
	registry.Register(testProvider(&opened, &closed))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	view := makeClosedManagerView(t, repo, registry, project.ID)
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > before.HeapAlloc+8*1024*1024 {
		t.Fatal("closed read-only view retained source secrets", after.HeapAlloc-before.HeapAlloc)
	}
	if _, err := view.List(t.Context(), "."); err == nil {
		t.Fatal("closed manager remained readable")
	}
	runtime.KeepAlive(view)
}
