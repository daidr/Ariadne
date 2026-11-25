package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
)

func TestArtifactFeedbackReportsStoredEditsAndCommentsOnce(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clock := time.UnixMilli(1900000000000)
	repo, err := core.Open(ctx, core.Options{Path: ":memory:", Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := repo.CreateArtifact(ctx, project.ID, core.NewArtifact{Kind: "document", Title: "Draft", Author: project.CoordinatorID, Content: "First"})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	clock = clock.Add(time.Second)
	_, err = repo.ReviseArtifact(ctx, a.ID, core.ArtifactRevision{
		Author: "user", BaseVersion: &a.Version, Content: "Revised first line",
		Shared: true, SharedState: []byte{1, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CommentOnArtifact(ctx, a.ID, "user", map[string]any{"type": "create", "threadId": "comment-thread",
		"commentId": "first-comment", "quote": "first line", "body": "Add an example"})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(api)
	defer server.Close()
	unauthClient := ariadnev1connect.NewArtifactServiceClient(server.Client(), server.URL+rpcPrefix)
	if _, err := unauthClient.SendArtifactFeedback(ctx, connect.NewRequest(&ariadnev1.SendArtifactFeedbackRequest{ArtifactId: a.ID})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected CodeUnauthenticated, got %v", err)
	}

	client := ariadnev1connect.NewArtifactServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("fixture")),
	)
	if _, err := client.SendArtifactFeedback(ctx, connect.NewRequest(&ariadnev1.SendArtifactFeedbackRequest{
		ArtifactId: a.ID,
		Images:     []string{"fil_other_project"},
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument for foreign image, got %v", err)
	}

	res, err := client.SendArtifactFeedback(ctx, connect.NewRequest(&ariadnev1.SendArtifactFeedbackRequest{ArtifactId: a.ID}))
	if err != nil {
		t.Fatalf("SendArtifactFeedback failed: %v", err)
	}
	fb := res.Msg.GetFeedback()
	if fb == nil {
		t.Fatal("expected non-nil feedback on first send")
	}
	if fb.GetNotify() != project.CoordinatorID {
		t.Fatalf("unexpected notify %q, want %q", fb.GetNotify(), project.CoordinatorID)
	}
	hasEdit, hasComment := false, false
	for _, item := range fb.GetItems() {
		if edit := item.GetEdit(); edit != nil && strings.Contains(edit.GetDiff(), "Revised first line") {
			hasEdit = true
		}
		if comment := item.GetComment(); comment != nil && strings.Contains(comment.GetBody(), "Add an example") {
			hasComment = true
		}
	}
	if !hasEdit || !hasComment {
		t.Fatalf("expected both edit and comment items, got %+v", fb.GetItems())
	}

	second, err := client.SendArtifactFeedback(ctx, connect.NewRequest(&ariadnev1.SendArtifactFeedbackRequest{ArtifactId: a.ID}))
	if err != nil {
		t.Fatalf("second SendArtifactFeedback failed: %v", err)
	}
	if second.Msg.GetFeedback() != nil {
		t.Fatalf("unchanged feedback went out again: %+v", second.Msg.GetFeedback())
	}
}

func TestWithdrawArtifactFeedbackRPC(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clock := time.UnixMilli(1900000000000)
	repo, err := core.Open(ctx, core.Options{Path: ":memory:", Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := repo.CreateArtifact(ctx, project.ID, core.NewArtifact{Kind: "document", Title: "Draft", Author: project.CoordinatorID, Content: "First"})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	clock = clock.Add(time.Second)
	if _, err := repo.ReviseArtifact(ctx, a.ID, core.ArtifactRevision{
		Author: "user", BaseVersion: &a.Version, Content: "Edited", Shared: true,
	}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(api)
	defer server.Close()
	client := ariadnev1connect.NewArtifactServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("fixture")),
	)

	sent, err := client.SendArtifactFeedback(ctx, connect.NewRequest(&ariadnev1.SendArtifactFeedbackRequest{ArtifactId: a.ID}))
	if err != nil || sent.Msg.GetFeedback() == nil {
		t.Fatalf("SendArtifactFeedback failed: %v, fb=%v", err, sent)
	}
	round := sent.Msg.GetFeedback().GetRound()

	withdrawRes, err := client.WithdrawArtifactFeedback(ctx, connect.NewRequest(&ariadnev1.WithdrawArtifactFeedbackRequest{
		ArtifactId: a.ID,
		Round:      round,
	}))
	if err != nil {
		t.Fatalf("WithdrawArtifactFeedback failed: %v", err)
	}
	if !withdrawRes.Msg.GetWithdrawn() {
		t.Fatal("expected withdrawn=true before turn starts")
	}

	// Re-send and start a turn -> second withdraw must return withdrawn=false.
	clock = clock.Add(time.Second)
	sent2, err := client.SendArtifactFeedback(ctx, connect.NewRequest(&ariadnev1.SendArtifactFeedbackRequest{ArtifactId: a.ID}))
	if err != nil || sent2.Msg.GetFeedback() == nil {
		t.Fatalf("second SendArtifactFeedback failed: %v", err)
	}
	clock = clock.Add(time.Second)
	if _, err := repo.StartTurn(ctx, project.CoordinatorID, "test-model"); err != nil {
		t.Fatal(err)
	}
	withdrawRes2, err := client.WithdrawArtifactFeedback(ctx, connect.NewRequest(&ariadnev1.WithdrawArtifactFeedbackRequest{
		ArtifactId: a.ID,
		Round:      sent2.Msg.GetFeedback().GetRound(),
	}))
	if err != nil {
		t.Fatalf("second WithdrawArtifactFeedback failed: %v", err)
	}
	if withdrawRes2.Msg.GetWithdrawn() {
		t.Fatal("expected withdrawn=false after turn started")
	}
}

func TestGetArtifactDocumentAskBaselineRPC(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clock := time.UnixMilli(1901000000000)
	repo, err := core.Open(ctx, core.Options{Path: ":memory:", Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	v1Doc := "# Spec\n\n```ask\n{\"id\":\"ask-1\",\"question\":\"DB?\",\"pick\":\"one\",\"options\":[{\"id\":\"pg\",\"label\":\"PostgreSQL\"},{\"id\":\"sqlite\",\"label\":\"SQLite\"}]}\n```\n"
	a, err := repo.CreateArtifact(ctx, project.ID, core.NewArtifact{
		Kind:    "document",
		Title:   "Spec",
		Author:  project.CoordinatorID,
		Content: v1Doc,
	})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	server := httptest.NewServer(api)
	defer server.Close()
	client := ariadnev1connect.NewArtifactServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("fixture")),
	)

	// Before answering: baseline is v1 with empty selection.
	got1, err := client.GetArtifact(ctx, connect.NewRequest(&ariadnev1.GetArtifactRequest{ArtifactId: a.ID}))
	if err != nil {
		t.Fatal(err)
	}
	if b := got1.Msg.GetAskBaseline(); b == nil || b.GetVersion() != 1 || len(b.GetAsks()) != 1 || len(b.GetAsks()[0].GetSelected()) != 0 {
		t.Fatalf("unexpected initial ask baseline: %+v", b)
	}

	// User answers without submitting -> baseline stays at v1.
	v2Doc := "# Spec\n\n```ask\n{\"id\":\"ask-1\",\"question\":\"DB?\",\"pick\":\"one\",\"options\":[{\"id\":\"pg\",\"label\":\"PostgreSQL\"},{\"id\":\"sqlite\",\"label\":\"SQLite\"}],\"selected\":[\"pg\"],\"note\":\"multi-node\"}\n```\n"
	clock = clock.Add(time.Second)
	if _, err := repo.ReviseArtifact(ctx, a.ID, core.ArtifactRevision{
		Author: "user", BaseVersion: &a.Version, Content: v2Doc, Shared: true,
	}); err != nil {
		t.Fatal(err)
	}
	got2, err := client.GetArtifact(ctx, connect.NewRequest(&ariadnev1.GetArtifactRequest{ArtifactId: a.ID}))
	if err != nil {
		t.Fatal(err)
	}
	if b := got2.Msg.GetAskBaseline(); b == nil || b.GetVersion() != 1 || len(b.GetAsks()[0].GetSelected()) != 0 {
		t.Fatalf("expected unsubmitted baseline to remain v1, got %+v", b)
	}

	// Submit feedback -> baseline advances to v2 with submitted answer.
	clock = clock.Add(time.Second)
	sent, err := client.SendArtifactFeedback(ctx, connect.NewRequest(&ariadnev1.SendArtifactFeedbackRequest{ArtifactId: a.ID}))
	if err != nil || sent.Msg.GetFeedback() == nil {
		t.Fatalf("SendArtifactFeedback failed: err=%v fb=%+v", err, sent)
	}
	got3, err := client.GetArtifact(ctx, connect.NewRequest(&ariadnev1.GetArtifactRequest{ArtifactId: a.ID}))
	if err != nil {
		t.Fatal(err)
	}
	b := got3.Msg.GetAskBaseline()
	if b == nil || b.GetVersion() != 2 || len(b.GetAsks()) != 1 {
		t.Fatalf("expected v2 baseline after submit, got %+v", b)
	}
	if ans := b.GetAsks()[0]; ans.GetId() != "ask-1" || len(ans.GetSelected()) != 1 || ans.GetSelected()[0] != "pg" || ans.GetNote() != "multi-node" {
		t.Fatalf("unexpected submitted ask baseline entry: %+v", ans)
	}

	// Withdraw -> baseline reverts to v1.
	clock = clock.Add(time.Second)
	if _, err := client.WithdrawArtifactFeedback(ctx, connect.NewRequest(&ariadnev1.WithdrawArtifactFeedbackRequest{
		ArtifactId: a.ID,
		Round:      sent.Msg.GetFeedback().GetRound(),
	})); err != nil {
		t.Fatal(err)
	}
	got4, err := client.GetArtifact(ctx, connect.NewRequest(&ariadnev1.GetArtifactRequest{ArtifactId: a.ID}))
	if err != nil {
		t.Fatal(err)
	}
	if b := got4.Msg.GetAskBaseline(); b == nil || b.GetVersion() != 1 || len(b.GetAsks()[0].GetSelected()) != 0 {
		t.Fatalf("expected baseline to revert to v1 after withdraw, got %+v", b)
	}
}
