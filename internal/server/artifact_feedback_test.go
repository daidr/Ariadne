package server

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
)

func TestArtifactFeedbackReportsStoredEditsAndCommentsOnce(t *testing.T) {
	ctx := t.Context()
	clock := time.UnixMilli(1900000000000)
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "test.sqlite"), Now: func() time.Time { return clock }})
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
