package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/sdk"
)

func TestRefreshRefusesToOverwriteEditMadeDuringFetch(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "dashboard.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	name := "project"
	project, err := repo.CreateProject(t.Context(), &name, core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	content := `{"version":1,"dashboard":{"version":1,"widgets":[]},"data":{},"source":{"kind":"fixture","settings":{"query":"issues"}}}`
	artifact, err := repo.CreateArtifact(t.Context(), project.ID, core.NewArtifact{Kind: "dashboard", Title: "Dashboard", Content: content, Author: "user"})
	if err != nil {
		t.Fatal(err)
	}
	registry := New()
	defer registry.Close(t.Context())
	remove, err := registry.Register(sdk.DashboardSource{Info: testInfo(), Fetch: func(ctx context.Context, _ map[string]any, _ string) (map[string]any, error) {
		title := "manual edit"
		if _, err := repo.ReviseArtifact(ctx, artifact.ID, core.ArtifactRevision{Content: content, Title: &title, Author: "user", BaseVersion: &artifact.Version}); err != nil {
			return nil, err
		}
		return map[string]any{"rows": []any{}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	old := registry.Get("fixture")
	_, err = Refresh(t.Context(), repo, registry, artifact.ID, RefreshOptions{Author: "user"})
	var conflict *domainerr.Error
	if !errors.As(err, &conflict) || conflict.Key != "error.artifactConflict" {
		t.Fatal("concurrent layout overwritten", err)
	}
	current, err := repo.GetArtifact(t.Context(), artifact.ID, nil)
	if err != nil || current.Title != "manual edit" || current.Content != content {
		t.Fatal("user edit changed", current, err)
	}
	remove()
	_, err = registry.Register(sdk.DashboardSource{Info: testInfo(), Fetch: func(context.Context, map[string]any, string) (map[string]any, error) {
		t.Error("old registration used replacement")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Fetch(t.Context(), nil, project.ID); err == nil {
		t.Fatal("stale registered source fetched replacement")
	}
	var decoded map[string]any
	if json.Unmarshal([]byte(current.Content), &decoded) != nil || decoded["takenAt"] != nil {
		t.Fatal("failed refresh saved a snapshot")
	}
}
