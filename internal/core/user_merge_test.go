package core

import (
	"path/filepath"
	"testing"
)

func TestUserMergeRecordsMethodAndResultInOneTransaction(t *testing.T) {
	repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "merge.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	name := "project"
	project, err := repo.CreateProject(t.Context(), &name, ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(t.Context(), project.ID, NewThread{Title: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.DB().ExecContext(t.Context(), `CREATE TRIGGER fail_merge BEFORE INSERT ON events WHEN NEW.type = 'branch.merged' BEGIN SELECT RAISE(ABORT, 'reject'); END`); err != nil {
		t.Fatal(err)
	}
	before, _ := repo.Store.LastSeq(t.Context())
	merge := map[string]any{"branch": "work", "base": "integration", "sha": "sha", "head": "head", "method": "merge"}
	if _, err := repo.RecordUserMerge(t.Context(), thread.ID, merge, "merge"); err == nil {
		t.Fatal("rejected merge succeeded")
	}
	after, _ := repo.Store.LastSeq(t.Context())
	project, _ = repo.GetProject(t.Context(), project.ID)
	current, _ := repo.GetThread(t.Context(), thread.ID)
	if before != after || project.Settings.Autopilot.Method != "squash" || current.Merged != nil {
		t.Fatal("failed merge partially committed")
	}
	if _, err := repo.Store.DB().ExecContext(t.Context(), "DROP TRIGGER fail_merge"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RecordUserMerge(t.Context(), thread.ID, merge, "merge"); err != nil {
		t.Fatal(err)
	}
	project, _ = repo.GetProject(t.Context(), project.ID)
	current, _ = repo.GetThread(t.Context(), thread.ID)
	if project.Settings.Autopilot.Method != "merge" || current.Merged == nil {
		t.Fatal("successful merge not recorded")
	}
}
