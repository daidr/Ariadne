package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

type interruptedExport struct {
	writes int
	cancel context.CancelFunc
}
type mutateDuringExport struct {
	bytes.Buffer
	once   bool
	change func()
}

func (writer *mutateDuringExport) Write(data []byte) (int, error) {
	if !writer.once {
		writer.once = true
		writer.change()
	}
	return writer.Buffer.Write(data)
}
func TestExportRemainsConsistentWhileProjectChanges(t *testing.T) {
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "snapshot.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	name := "before"
	project, err := repo.CreateProject(t.Context(), &name, ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := repo.Store.LastSeq(t.Context())
	writer := &mutateDuringExport{change: func() {
		if _, err := repo.RenameProject(t.Context(), project.ID, "after"); err != nil {
			t.Error(err)
		}
	}}
	if err := repo.ExportProject(t.Context(), project.ID, writer, 123); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Project struct{ Name string }
		Events  []struct{ Seq int64 }
	}
	if err := json.Unmarshal(writer.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Project.Name != "before" || len(result.Events) == 0 || result.Events[len(result.Events)-1].Seq != before {
		t.Fatal("export mixed snapshots", writer.String())
	}
	current, _ := repo.GetProject(t.Context(), project.ID)
	if current.Name != "after" {
		t.Fatal("test mutation did not happen")
	}
}

func (writer *interruptedExport) Write(data []byte) (int, error) {
	writer.writes++
	if writer.writes == 2 {
		writer.cancel()
		return 0, io.ErrClosedPipe
	}
	return len(data), nil
}
func TestInterruptedExportReleasesSnapshotAndDoesNotChangeData(t *testing.T) {
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "export.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	name := "project"
	project, err := repo.CreateProject(t.Context(), &name, ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := repo.Store.LastSeq(t.Context())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := &interruptedExport{cancel: cancel}
	if err := repo.ExportProject(ctx, project.ID, writer, 123); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("export hid write failure", err)
	}
	after, _ := repo.Store.LastSeq(t.Context())
	if before != after {
		t.Fatal("export changed durable events")
	}
	if err := repo.DeleteProject(t.Context(), project.ID); err != nil {
		t.Fatal("interrupted export retained transaction", err)
	}
	if stats := repo.Store.DB().Stats(); stats.InUse != 0 {
		t.Fatal("export retained database connection", stats.InUse)
	}
}
