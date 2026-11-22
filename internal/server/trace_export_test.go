package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/modelstep"
	"github.com/daidr/Ariadne/sdk"
)

func TestTraceExportIncludesFailedRequestsAndRedactsCredentials(t *testing.T) {
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: filepath.Join(t.TempDir(), "trace.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("fixture"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	id := project.CoordinatorID
	other, err := repo.CreateProject(ctx, new("another project"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	post := func(project, id, text string) {
		t.Helper()
		if _, err := repo.PostMessage(ctx, project, core.MessageInput{ConversationID: id, Author: "user", Content: text}); err != nil {
			t.Fatal(err)
		}
	}
	post(other.ID, other.CoordinatorID, "other project only")
	for i := range 130 {
		post(project.ID, id, fmt.Sprintf("message %03d", i))
	}
	const token = "synthetic-export-auth-secret"
	for i := range 12 {
		turn, err := repo.StartTurn(ctx, id, "ACP")
		if err != nil {
			t.Fatal(err)
		}
		input := core.StepInput{System: "fixture instructions", Tools: "[]", Call: `{"provider":"acp","model":"fixture-model","settings":{"command":"agy acp --api-key secret-cli"}}`}
		failure := modelstep.AttemptFailure{Attempt: 1, StartedAt: 10, FinishedAt: 20, Detail: fmt.Sprintf("error %02d: %s", i, token), AgentError: &sdk.AgentError{
			Operation: "session/prompt", Version: "0.9.0", RPCCode: new(-32603), RPCData: `{"requestId":"req-42","accessToken":"secret-data"}`,
			Stderr: "before\nCookie: cookie-secret\nafter\nreply {\"token\":\"inline-secret\"}",
		}}
		if err := repo.RecordModelFailure(ctx, id, turn, 5, input, "step", "", failure); err != nil {
			t.Fatal(err)
		}
		if err := repo.FinishTurn(ctx, id, turn, "failed", domainerr.New("error.modelRequest", map[string]any{"detail": "failed"})); err != nil {
			t.Fatal(err)
		}
	}
	api, err := New(Options{Repository: repo, Token: token, Version: "test-version"})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	get := func(path string, authenticate bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil).WithContext(ctx)
		if authenticate {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	route := "/api/conversations/" + id + "/trace"
	if w := get(route+"/export", false); w.Code != 401 {
		t.Fatalf("anonymous export: %d", w.Code)
	}
	if w := get("/api/conversations/missing/trace/export", true); w.Code != 404 {
		t.Fatalf("missing conversation: %d", w.Code)
	}
	srv := httptest.NewServer(api)
	defer srv.Close()
	convClient := ariadnev1connect.NewConversationServiceClient(srv.Client(), srv.URL+rpcPrefix, connect.WithInterceptors(bearerClientInterceptor(token)))
	traceResp, err := convClient.GetTrace(ctx, connect.NewRequest(&ariadnev1.GetTraceRequest{ConversationId: id}))
	if err != nil {
		t.Fatalf("get trace failed: %v", err)
	}
	hasFailed := false
	for _, turn := range traceResp.Msg.Turns {
		for _, item := range turn.Items {
			if item.GetRequestFailed() != nil {
				hasFailed = true
			}
		}
	}
	if !hasFailed {
		t.Fatalf("failed requests absent from the trace: %+v", traceResp.Msg)
	}
	w := get(route+"/export", true)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("export response: %d %v", w.Code, w.Header())
	}
	archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, file := range archive.File {
		if file.Name == "" {
			t.Fatal("unnamed ZIP entry")
		}
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(bytes.TrimSpace(body), []byte{'\n'}) {
			if !json.Valid(line) {
				t.Fatalf("invalid JSON in %s", file.Name)
			}
		}
		parts[file.Name] = string(body)
		for _, forbidden := range []string{token, "secret-cli", "secret-data", "cookie-secret", "inline-secret", "other project only"} {
			if strings.Contains(parts[file.Name], forbidden) {
				t.Fatalf("%s exported out of scope content %q", file.Name, forbidden)
			}
		}
	}
	if len(parts) != 4 || !strings.Contains(parts["manifest.json"], "test-version") ||
		strings.Count(parts["events.ndjson"], `"type":"conversation.model_failed"`) != 12 ||
		!strings.Contains(parts["events.ndjson"], "req-42") || !strings.Contains(parts["events.ndjson"], "-32603") ||
		!strings.Contains(parts["events.ndjson"], "error 00") || !strings.Contains(parts["events.ndjson"], "error 11") ||
		!strings.Contains(parts["transcript.ndjson"], "message 129") ||
		!strings.Contains(parts["inputs.ndjson"], "fixture instructions") || strings.Count(parts["inputs.ndjson"], "\n") != 3 {
		t.Fatal("incomplete ZIP: expected diagnostic events, complete transcript and deduplicated inputs")
	}
}
