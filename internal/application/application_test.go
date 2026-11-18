package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/daidr/Ariadne/internal/config"
	"github.com/daidr/Ariadne/internal/eventstore"
)

func TestNativeHTTPExecutesTurnAndRebuildsRequests(t *testing.T) {
	const userText = "请用模拟服务回复，并把外部交付要求设为通过集成平台交付"
	const newText = "这条是模型请求发出之后补充的"
	const replyText = "这是 Go 后端写入的回复。"
	const secret = "fixture-provider-secret"
	tipSeen := make(chan struct{})
	unblock := make(chan struct{})
	var steps atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("mock got the wrong path or auth")
		}
		var request struct {
			Tools    []any            `json:"tools"`
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(value map[string]any) {
			data, _ := json.Marshal(value)
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		choice := func(delta map[string]any, finish any) {
			send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		}
		if len(request.Tools) == 0 {
			choice(map[string]any{"content": "model check"}, "stop")
		} else {
			switch steps.Add(1) {
			case 1:
				content, _ := json.Marshal(request.Messages)
				if !bytes.Contains(content, []byte(userText)) {
					t.Error("first request did not include the user message")
				}
				close(tipSeen)
				select {
				case <-unblock:
				case <-r.Context().Done():
					return
				}
				args, _ := json.Marshal(map[string]any{"text": replyText})
				function := map[string]any{"name": "send_message", "arguments": string(args)}
				call := map[string]any{"index": 0, "id": "reply-1", "type": "function", "function": function}
				delivery := map[string]any{"index": 1, "id": "set-delivery-1", "type": "function", "function": map[string]any{
					"name": "set_delivery", "arguments": `{"mode":"external","instructions":"通过集成平台交付"}`,
				}}
				choice(map[string]any{"tool_calls": []any{call, delivery}}, nil)
				choice(map[string]any{}, "tool_calls")
			case 2:
				messages, _ := json.Marshal(request.Messages)
				if !bytes.Contains(messages, []byte(newText)) || !bytes.Contains(messages, []byte("\"tool_call_id\":\"reply-1\"")) {
					t.Error("second request missed the new user message or the tool result")
				}
				choice(map[string]any{}, "stop")
			default:
				t.Error("turn was unexpectedly started again")
				choice(map[string]any{}, "stop")
			}
			send(map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 23, "completion_tokens": 6}})
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer model.Close()
	home := t.TempDir()
	cfg := config.Config{Home: home, DatabasePath: filepath.Join(home, "ariadne.sqlite"), Token: "fixture-session"}
	open := func() *Application {
		t.Helper()
		app, err := Open(t.Context(), Options{Config: cfg, Client: model.Client(), Web: fstest.MapFS{"index.html": {Data: []byte("web shell")}}, OnError: func(err error) { t.Error(err) }})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := app.Close(ctx); err != nil {
				t.Error(err)
			}
		})
		return app
	}
	first := open()
	defer first.Close(context.Background())
	httpServer := httptest.NewServer(first.Handler)
	defer httpServer.Close()
	request := func(method, path string, body any, token string, status int) []byte {
		t.Helper()
		data, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(t.Context(), method, httpServer.URL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := httpServer.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err = io.ReadAll(res.Body)
		if err != nil || res.StatusCode != status {
			t.Fatalf("%s %s: %d %s %v", method, path, res.StatusCode, data, err)
		}
		return data
	}
	request("GET", "/api/health", nil, "", 200)
	request("GET", "/api/projects", nil, "", 401)
	if string(request("GET", "/", nil, "", 200)) != "web shell" {
		t.Fatal("native server did not serve configured frontend assets")
	}
	credentials := map[string]any{"baseUrl": model.URL + "/v1", "apiKey": secret}
	providers := []any{map[string]any{"id": "p", "kind": "openai-compatible", "name": "Mock", "settings": credentials}}
	models := []any{map[string]any{"id": "m", "provider": "p", "model": "fixture"}}
	roles := map[string]any{"thread": map[string]any{"model": "m"}}
	config := request("PUT", "/api/settings/models", map[string]any{"providers": providers, "models": models, "roles": roles}, cfg.Token, 200)
	if bytes.Contains(config, []byte(secret)) {
		t.Fatal("native settings included the provider secret")
	}
	var created struct {
		Project struct {
			ID          string `json:"id"`
			Coordinator string `json:"coordinatorId"`
		} `json:"project"`
	}
	if err := json.Unmarshal(request("POST", "/api/projects", map[string]any{"name": "Fixture", "message": userText}, cfg.Token, 201), &created); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tipSeen:
	case <-time.After(8 * time.Second):
		t.Fatal("coordinator never reached mock model")
	}
	project, conversation := created.Project.ID, created.Project.Coordinator
	request("POST", "/api/projects/"+project+"/messages", map[string]any{"conversationId": conversation, "content": newText}, cfg.Token, 201)
	close(unblock)
	deadline := time.Now().Add(8 * time.Second)
	for {
		view, err := first.Repository.GetConversation(t.Context(), conversation)
		if err != nil {
			t.Fatal(err)
		}
		if view.Turn == nil && view.LastOutcome == "completed" && view.PendingSeq == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn did not finish: %+v", view)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var seqs []int64
	all, err := first.Repository.Store.Read(t.Context(), eventstore.ReadOptions{Limit: 256})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range all {
		if item.Type == "turn.step" {
			seqs = append(seqs, item.Seq)
		}
	}
	if len(seqs) != 2 || steps.Load() != 2 {
		t.Fatalf("wanted two durable model steps, got %v, %d requests", seqs, steps.Load())
	}
	settings, err := first.Repository.GetProject(t.Context(), project)
	if err != nil || settings.Settings.Delivery.Mode != "external" || settings.Settings.Delivery.Instructions != "通过集成平台交付" {
		for _, item := range all {
			if item.Type == "tool.finished" {
				t.Log(string(item.Data))
			}
		}
		t.Fatal("coordinator could not save the user's requested delivery settings", settings.Settings.Delivery, err)
	}
	rebuild := func(seq int64) string {
		return string(request("GET", "/api/conversations/"+conversation+"/trace/requests/"+strconv.FormatInt(seq, 10), nil, cfg.Token, 200))
	}
	early, later := rebuild(seqs[0]), rebuild(seqs[1])
	if !strings.Contains(early, userText) || strings.Contains(early, newText) || !strings.Contains(later, newText) ||
		!strings.Contains(later, "reply-1") || strings.Contains(early+later, secret) {
		t.Fatal("trace did not reproduce the snapshots or redact credentials")
	}
	httpServer.Close()
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	second := open()
	messages, err := second.Repository.ListMessages(t.Context(), conversation)
	if err != nil || len(messages) != 3 || messages[2].Content != replyText || messages[2].Author != "coordinator" {
		t.Fatal("reopened database did not retain the reply", messages, err)
	}
}
