package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
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
	api, err := New(Options{Repository: repo, Token: "test-token", Now: now, Models: builtins.Registry})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	unauth := ariadnev1connect.NewConversationServiceClient(srv.Client(), srv.URL+rpcPrefix)
	client := ariadnev1connect.NewConversationServiceClient(srv.Client(), srv.URL+rpcPrefix, connect.WithInterceptors(bearerClientInterceptor("test-token")))

	if _, err := unauth.GetTraceRequestDetail(t.Context(), connect.NewRequest(&ariadnev1.GetTraceRequestDetailRequest{ConversationId: "coo_a", Seq: 1})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated, got %v", err)
	}
	for i, event := range seed {
		if event.Type == "turn.step" || event.Type == "research.step" || event.Type == "conversation.compacted" {
			resp, err := client.GetTraceRequestDetail(t.Context(), connect.NewRequest(&ariadnev1.GetTraceRequestDetailRequest{
				ConversationId: "coo_a",
				Seq:            int64(i + 1),
			}))
			if err != nil {
				t.Fatalf("seq %d (%s) failed: %v", i+1, event.Type, err)
			}
			if resp.Msg.GetRequest() == nil {
				t.Fatalf("seq %d (%s) returned nil request", i+1, event.Type)
			}
		}
	}
	if _, err := client.GetTraceRequestDetail(t.Context(), connect.NewRequest(&ariadnev1.GetTraceRequestDetailRequest{ConversationId: "coo_a", Seq: 0})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for seq=0, got %v", err)
	}
	if _, err := client.GetTraceRequestDetail(t.Context(), connect.NewRequest(&ariadnev1.GetTraceRequestDetailRequest{ConversationId: "coo_a", Seq: 1})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected not found for non-step seq=1, got %v", err)
	}
	if _, err := client.GetTraceRequestDetail(t.Context(), connect.NewRequest(&ariadnev1.GetTraceRequestDetailRequest{ConversationId: "other", Seq: 7})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected not found for unknown conversation, got %v", err)
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
