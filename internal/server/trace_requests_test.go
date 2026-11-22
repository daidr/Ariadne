package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/trace"
)

func requestTraceSeed() []eventstore.NewEvent {
	seed := traceSeed()
	project := seed[0]
	event := func(kind string, data any) eventstore.NewEvent {
		return eventstore.NewEvent{Type: kind, ProjectID: "prj_a", Data: json.RawMessage(encoded(data))}
	}
	seed = append([]eventstore.NewEvent{project, event("message.posted", object{"conversationId": "coo_a", "messageId": "msg_user", "author": "user", "kind": "chat", "content": "original words", "attachments": []any{object{"fileId": "fil_image", "name": "picture.png", "mime": "image/png", "size": 3}, object{"fileId": "fil_text", "name": "notes.txt", "mime": "text/plain", "size": 8}}}), event("prompt.recorded", object{"hash": "system", "content": "fixed system instructions"}), event("prompt.recorded", object{"hash": "tools", "content": `[{"name":"read_file","description":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]`})}, seed[1:]...)
	lastTool := 0
	edited := false
	output := []eventstore.NewEvent{}
	for _, item := range seed {
		var data object
		json.Unmarshal(item.Data, &data)
		if item.Type == "prompt.recorded" && data["hash"] == "call" {
			data["content"] = encoded(object{"provider": "openai-compatible", "model": "gpt-6.1", "effort": "high", "settings": object{"baseUrl": "https://api.openai.com/v1"}, "maxTokens": 2000, "extraBody": object{"temperature": 0.3}})
		}
		if item.Type == "turn.step" || item.Type == "conversation.compacted" {
			data["sawSeq"] = len(output)
			if item.Type == "conversation.compacted" {
				data["upTo"] = lastTool
			}
		}
		item.Data = json.RawMessage(encoded(data))
		output = append(output, item)
		if item.Type == "tool.finished" {
			lastTool = len(output)
			if !edited {
				edited = true
				output = append(output, event("message.edited", object{"messageId": "msg_user", "author": "user", "conversationId": "coo_a", "content": "corrected later", "reason": "edit"}))
			}
		}
	}
	return output
}
func TestTraceRequestBodiesWithoutCallingModels(t *testing.T) {
	seed := requestTraceSeed()
	no := false
	requests := []apiRequest{{Method: "GET", Path: "/api/conversations/coo_a/trace/requests/1", Auth: &no}}
	for i, event := range seed {
		if event.Type == "turn.step" || event.Type == "research.step" || event.Type == "conversation.compacted" {
			requests = append(requests, apiRequest{Method: "GET", Path: "/api/conversations/coo_a/trace/requests/" + strconv.Itoa(i+1)})
		}
	}
	for _, path := range []string{"/api/conversations/coo_a/trace/requests/1", "/api/conversations/coo_a/trace/requests/0", "/api/conversations/coo_a/trace/requests/bad", "/api/conversations/other/trace/requests/7"} {
		requests = append(requests, apiRequest{Method: "GET", Path: path})
	}
	imageHash := sha256.Sum256([]byte{0, 255, 8})
	textHash := sha256.Sum256([]byte{239, 187, 191, 104, 101, 108, 108, 111})
	sql := fmt.Sprintf(`INSERT INTO files VALUES ('fil_image','prj_a','image/png',3,'%s',x'00ff08',0); INSERT INTO files VALUES ('fil_text','prj_a','text/plain',8,'%s',x'efbbbf68656c6c6f',0)`, hex.EncodeToString(imageHash[:]), hex.EncodeToString(textHash[:]))
	now := func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "requests.sqlite"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.DB().ExecContext(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
	builtins, err := models.NewBuiltins(models.BuiltinOptions{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer builtins.Close(context.Background())
	for _, provider := range models.HTTPBuiltinProviders(models.HTTPOptions{}) {
		remove, err := builtins.Registry.Register(provider)
		if err != nil {
			t.Fatal(err)
		}
		defer remove()
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Now: now, Models: builtins.Registry})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	for _, item := range requests {
		r := httptest.NewRequest(item.Method, item.Path, nil)
		if item.Auth == nil || *item.Auth {
			r.Header.Set("Authorization", "Bearer test-token")
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err, response.Body.String())
		}
		if response.Code >= 500 {
			t.Fatal(response.Body.String())
		}
	}
	for i, item := range seed {
		if item.Type == "turn.step" {
			detail, _, err := trace.RebuildRequest(t.Context(), repo, "coo_a", int64(i+1))
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(detail.Messages)
			if !strings.Contains(string(data), "original words") || strings.Contains(string(data), "corrected later") {
				t.Fatal("request history used a later edit", string(data))
			}
			break
		}
	}
}
