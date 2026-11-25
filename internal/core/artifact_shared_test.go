package core

import (
	"bytes"
	"testing"
	"time"
)

func TestSharedArtifactVersionAndStateCommitTogether(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := Open(ctx, Options{Path: ":memory:"})
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

func TestCompletedModelTurnKeepsFeedbackOpenUntilAddressed(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clock := time.UnixMilli(1900000000000)
	repo, err := Open(ctx, Options{Path: ":memory:", Now: func() time.Time { return clock }})
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
	send := func(id, body string) {
		t.Helper()
		clock = clock.Add(time.Second)
		action := object{"type": "create", "threadId": id, "commentId": id + "-user", "quote": "Draft", "body": body}
		if _, err := repo.CommentOnArtifact(ctx, artifact.ID, "user", action); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.SendPendingArtifactFeedback(ctx, artifact.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	send("comment_a", "Please explain this.")
	turn, err := repo.StartTurn(ctx, project.CoordinatorID, "model")
	if err != nil {
		t.Fatal(err)
	}
	send("comment_b", "What does this mean?")
	if err := repo.FinishTurn(ctx, project.CoordinatorID, turn, "completed", nil); err != nil {
		t.Fatal(err)
	}
	rounds, err := repo.ListArtifactFeedback(ctx, artifact.ID)
	if err != nil || len(rounds) != 2 {
		t.Fatal(rounds, err)
	}
	if rounds[0]["doneAt"] != nil || rounds[1]["doneAt"] != nil {
		t.Fatal("empty turn settled feedback", rounds)
	}
	if _, err := repo.FinishArtifactFeedback(ctx, artifact.ID, 1, project.CoordinatorID, "Answered."); err == nil {
		t.Fatal("unanswered comment did not prevent closing feedback")
	}
	if _, err := repo.CommentOnArtifact(ctx, artifact.ID, project.CoordinatorID, object{
		"type": "reply", "threadId": "comment_a", "commentId": "comment_a-agent", "body": "This is the first draft.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FinishArtifactFeedback(ctx, artifact.ID, 1, project.CoordinatorID, "Explained the draft."); err != nil {
		t.Fatal(err)
	}
	rounds, err = repo.ListArtifactFeedback(ctx, artifact.ID)
	if err != nil || rounds[0]["doneAt"] != nil || rounds[1]["doneBy"] != project.CoordinatorID {
		t.Fatal("explicit finish settled the wrong feedback", rounds, err)
	}
}
