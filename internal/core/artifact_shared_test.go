package core

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

func TestSharedArtifactVersionAndStateCommitTogether(t *testing.T) {
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "artifact.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Fixture"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := repo.CreateArtifact(ctx, project.ID, NewArtifact{Kind: "document", Title: "Draft", Author: "user", Content: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	input := ArtifactRevision{Author: "user", BaseVersion: &artifact.Version, Content: "v2", Shared: true, SharedState: []byte{1, 2, 3}}
	if _, err := repo.DB.ExecContext(ctx, `CREATE TRIGGER fail_shared BEFORE INSERT ON artifact_shared
		BEGIN SELECT RAISE(ABORT, 'simulated shared state write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReviseArtifact(ctx, artifact.ID, input); err == nil {
		t.Fatal("shared state failure did not fail the edit")
	}
	got, err := repo.GetArtifact(ctx, artifact.ID, nil)
	if err != nil || got.Version != artifact.Version || got.Content != "v1" {
		t.Fatal("shared state failure left a durable revision", got, err)
	}
	if _, err := repo.DB.ExecContext(ctx, "DROP TRIGGER fail_shared"); err != nil {
		t.Fatal(err)
	}
	got, err = repo.ReviseArtifact(ctx, artifact.ID, input)
	if err != nil || got.Version != artifact.Version+1 || got.Content != "v2" {
		t.Fatal("shared version failed", got, err)
	}
	state, err := repo.ReadSharedState(ctx, artifact.ID)
	if err != nil || state == nil || state.Version != got.Version || !bytes.Equal(state.State, input.SharedState) {
		t.Fatal("version mismatch in durable room state", state, err)
	}
	input.SharedState = []byte{4, 5, 6}
	if _, err := repo.ReviseArtifact(ctx, artifact.ID, input); err == nil {
		t.Fatal("stale room overwrote the current state")
	}
	state, err = repo.ReadSharedState(ctx, artifact.ID)
	if err != nil || state == nil || !bytes.Equal(state.State, []byte{1, 2, 3}) {
		t.Fatal("state changed after a stale write", state, err)
	}
}

func TestCompletedModelTurnSettlesOnlyFeedbackItStartedWith(t *testing.T) {
	ctx := t.Context()
	clock := time.UnixMilli(1900000000000)
	repo, err := Open(ctx, Options{Path: filepath.Join(t.TempDir(), "artifact.sqlite"), Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Fixture"), ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := repo.CreateArtifact(ctx, project.ID, NewArtifact{Kind: "document", Title: "Draft", Author: project.CoordinatorID, Content: "Draft"})
	if err != nil {
		t.Fatal(err)
	}
	send := func() {
		t.Helper()
		clock = clock.Add(time.Second)
		if _, err := repo.SendArtifactFeedback(ctx, artifact.ID, ArtifactFeedback{Items: []any{object{"kind": "edit", "diff": "+ Added"}}}); err != nil {
			t.Fatal(err)
		}
	}
	send()
	turn, err := repo.StartTurn(ctx, project.CoordinatorID, "model")
	if err != nil {
		t.Fatal(err)
	}
	send()
	if err := repo.CompleteModelTurn(ctx, project.CoordinatorID, turn); err != nil {
		t.Fatal(err)
	}
	rounds, err := repo.ListArtifactFeedback(ctx, artifact.ID)
	if err != nil || len(rounds) != 2 {
		t.Fatal(rounds, err)
	}
	if rounds[0]["doneAt"] != nil || rounds[1]["doneBy"] != project.CoordinatorID {
		t.Fatal("turn settled feedback outside its start time", rounds)
	}
}
