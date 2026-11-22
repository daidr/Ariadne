package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/realtime"
)

func TestEventStreamReconnectIncludesCurrentLiveBackgroundAndDraftState(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	hub := realtime.NewHub()
	defer hub.Close()
	state := realtime.NewState(hub)
	defer state.Close()
	live := realtime.NewLive(state, realtime.LiveOptions{})
	defer live.Close()
	drafts := realtime.NewDrafts(state, realtime.LiveOptions{})
	defer drafts.Close()
	if _, err := live.Start(realtime.LiveCommand{ProjectID: "p", ConversationID: "c", Command: "build", CWD: "."}); err != nil {
		t.Fatal(err)
	}
	if _, err := live.Start(realtime.LiveCommand{ProjectID: "other", ConversationID: "else", Command: "private", CWD: "."}); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(object{"projectId": "p", "conversationId": "c", "commands": []object{{"id": "cmd-1", "command": "serve", "startedAt": 1, "url": nil, "result": nil}}})
	if err := state.Set(realtime.Update{Kind: "background", ProjectID: "p", ConversationID: "c", Data: data}, true); err != nil {
		t.Fatal(err)
	}
	draft, err := drafts.Start("p", "c", "message")
	if err != nil {
		t.Fatal(err)
	}
	if err := draft.Write("回复草稿"); err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "secret", State: state})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	finished := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.ServeHTTP(w, r)
		select {
		case finished <- struct{}{}:
		default:
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	client := ariadnev1connect.NewEventServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("secret")),
	)
	projectID := "p"
	stream, err := client.StreamEvents(ctx, connect.NewRequest(&ariadnev1.StreamEventsRequest{
		ProjectId: &projectID,
	}))
	if err != nil {
		t.Fatal(err)
	}

	if !stream.Receive() {
		t.Fatalf("expected ready frame, err=%v", stream.Err())
	}
	if stream.Msg().GetReady() == nil {
		t.Fatalf("expected ready first, got %+v", stream.Msg())
	}

	var gotLive, gotBackground, gotDraft bool
	for range 3 {
		if !stream.Receive() {
			t.Fatalf("expected transient snapshot frame, err=%v", stream.Err())
		}
		switch payload := stream.Msg().Payload.(type) {
		case *ariadnev1.StreamEventsResponse_Live:
			if payload.Live.GetProjectId() != "p" || payload.Live.GetCommand().GetCommand() != "build" {
				t.Fatalf("unexpected live snapshot: %+v", payload.Live)
			}
			gotLive = true
		case *ariadnev1.StreamEventsResponse_Background:
			if payload.Background.GetProjectId() != "p" || len(payload.Background.GetCommands()) != 1 || payload.Background.GetCommands()[0].GetId() != "cmd-1" {
				t.Fatalf("unexpected background snapshot: %+v", payload.Background)
			}
			gotBackground = true
		case *ariadnev1.StreamEventsResponse_Draft:
			if payload.Draft.GetProjectId() != "p" || payload.Draft.GetDraft().GetText() != "回复草稿" {
				t.Fatalf("unexpected draft snapshot: %+v", payload.Draft)
			}
			gotDraft = true
		default:
			t.Fatalf("unexpected initial frame: %+v", stream.Msg())
		}
	}
	if !gotLive || !gotBackground || !gotDraft {
		t.Fatalf("missing initial snapshots: live=%v background=%v draft=%v", gotLive, gotBackground, gotDraft)
	}

	cancel()
	_ = stream.Close()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("event stream state subscription retained after disconnect")
	}
}
