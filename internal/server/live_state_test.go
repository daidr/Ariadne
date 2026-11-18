package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/realtime"
)

func TestSSEReconnectIncludesCurrentLiveBackgroundAndDraftState(t *testing.T) {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { api.ServeHTTP(w, r); finished <- struct{}{} }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/events?projectId=p", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer secret")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("SSE failed", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	for _, kind := range []string{"live", "background", "draft"} {
		var frame strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			frame.WriteString(line)
			if line == "\n" {
				break
			}
		}
		text := frame.String()
		if !strings.Contains(text, "event: "+kind+"\n") || !strings.Contains(text, `"projectId":"p"`) || strings.Contains(text, "private") {
			t.Fatalf("wrong initial %s frame: %s", kind, text)
		}
	}
	cancel()
	response.Body.Close()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("SSE state subscription retained after disconnect")
	}
}
