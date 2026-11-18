package compat

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/eventstore"
)

type snapshot struct {
	Projects []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"projects"`
	Events  []eventstore.Event `json:"events"`
	Exports json.RawMessage    `json:"exports"`
	Shared  json.RawMessage    `json:"shared"`
	Files   json.RawMessage    `json:"files"`
	Version int                `json:"version"`
}

// This projection checks both directions of the SQLite boundary. It is not the
// application's projects projection; that migration has its own acceptance work.
type renameProjection struct{}

func (renameProjection) Setup(context.Context, *sql.Tx) error          { return nil }
func (renameProjection) Forget(context.Context, *sql.Tx, string) error { return nil }
func (renameProjection) Apply(ctx context.Context, tx *sql.Tx, event eventstore.Event) error {
	var data struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE projects SET name = ?, unnamed = 0, updated_at = ? WHERE id = ?", data.Name, event.At, event.ProjectID)
	return err
}

func TestLegacySQLiteRoundTrip(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Skip("legacy compatibility oracle needs Bun for tests only")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "旧数据.sqlite")
	oracle := func(mode, path string) snapshot {
		t.Helper()
		cmd := legacytest.Command(t, bun, "packages/agent/src/testing/go-backend-fixture.ts", mode, path)
		cmd.Dir = root
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("legacy %s: %v\n%s", mode, err, output)
		}
		var result snapshot
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatalf("decode oracle: %v\n%s", err, output)
		}
		return result
	}
	before := oracle("create", path)
	store, err := eventstore.Open(t.Context(), eventstore.Options{
		Path: path, Projections: []eventstore.Projection{renameProjection{}},
		Now: func() time.Time { return time.UnixMilli(1800000000001) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := store.Read(t.Context(), eventstore.ReadOptions{})
	if err != nil || !reflect.DeepEqual(events, before.Events) {
		t.Fatalf("legacy events changed: %v", err)
	}
	projectID := before.Projects[0].ID
	if _, err := store.Append(t.Context(), []eventstore.NewEvent{{
		Type: "project.renamed", ProjectID: projectID, Data: json.RawMessage(`{"name":"Go 更新后的项目"}`),
	}}); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "native-backup.sqlite")
	if err := store.Backup(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, backup} {
		after := oracle("read", candidate)
		if after.Version != before.Version {
			t.Fatalf("schema version changed: %d -> %d", before.Version, after.Version)
		}
		if len(after.Projects) != 1 || after.Projects[0].Name != "Go 更新后的项目" {
			t.Fatalf("legacy cannot read native projection: %+v", after.Projects)
		}
		if !reflect.DeepEqual(after.Events[:len(before.Events)], before.Events) {
			t.Fatal("native write changed earlier events")
		}
		if len(after.Events) != len(before.Events)+1 || after.Events[len(before.Events)].Type != "project.renamed" {
			t.Fatal("legacy cannot read native event")
		}
		if string(after.Files) != string(before.Files) || string(after.Shared) != string(before.Shared) {
			t.Fatal("file or Yjs bytes changed")
		}
		if len(after.Exports) == 0 {
			t.Fatal("legacy export failed")
		}
	}
}
