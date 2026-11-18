package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
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
	request := func(body, token string, status int) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/artifacts/"+a.ID+"/feedback", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res := httptest.NewRecorder()
		api.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("feedback returned %d: %s", res.Code, res.Body.String())
		}
		return res.Body.String()
	}
	request("{}", "", 401)
	request(`{"images":[8]}`, "fixture", 400)
	request(`{"images":["fil_other_project"]}`, "fixture", 400)
	body := request("{}", "fixture", 200)
	for _, needle := range []string{`"kind":"edit"`, `"kind":"comment"`, "Revised first line", "Add an example", project.CoordinatorID} {
		if !strings.Contains(body, needle) {
			t.Fatalf("missing %s in feedback %s", needle, body)
		}
	}
	body = request("{}", "fixture", 200)
	if !strings.Contains(body, `"feedback":null`) {
		t.Fatalf("unchanged feedback went out again: %s", body)
	}
}
