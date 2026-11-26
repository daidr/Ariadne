package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/sdk"
)

func TestClaudeEnvChild(t *testing.T) {
	if !slices.Contains(os.Args, "--claude-env-child") {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var msg map[string]any
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		switch msg["type"] {
		case "control_request":
			id, _ := msg["request_id"].(string)
			resp := map[string]any{
				"type": "control_response",
				"response": map[string]any{
					"subtype":    "success",
					"request_id": id,
					"response": map[string]any{
						"models": []map[string]any{
							{"value": "env-" + os.Getenv("CLAUDE_TEST_TAG"), "displayName": os.Getenv("CLAUDE_TEST_TAG")},
						},
					},
				},
			}
			data, _ := json.Marshal(resp)
			fmt.Fprintln(os.Stdout, string(data))
			os.Exit(0)
		case "user":
			summary := fmt.Sprintf("tag=%s timeout=%s tokens=%s ariadne=%s",
				os.Getenv("CLAUDE_TEST_TAG"),
				os.Getenv("MCP_TOOL_TIMEOUT"),
				os.Getenv("MAX_MCP_OUTPUT_TOKENS"),
				os.Getenv("ARIADNE_SECRET"),
			)
			assistant, _ := json.Marshal(map[string]any{
				"type": "assistant",
				"message": map[string]any{
					"id":      "msg-1",
					"content": []map[string]any{{"type": "text", "text": summary}},
				},
			})
			fmt.Fprintln(os.Stdout, string(assistant))
			fmt.Fprintln(os.Stdout, `{"type":"result","subtype":"success"}`)
			os.Exit(0)
		}
	}
	os.Exit(0)
}

func claudeChildCommand(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`"%s" -test.run=^TestClaudeEnvChild$ -- --claude-env-child`, exe)
}

func TestClaudeCustomEnvPropagation(t *testing.T) {
	t.Parallel()
	cmd := claudeChildCommand(t)

	listed, err := ListModels(t.Context(), Settings{
		Command: cmd,
		Env:     map[string]string{"CLAUDE_TEST_TAG": "custom-list"},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != "env-custom-list" {
		t.Fatalf("ListModels with custom env = %+v", listed)
	}

	sessions, err := agentruntime.NewSessions(Kind(nil), agentruntime.SessionOptions{
		DataDir: t.TempDir(),
		Quiet:   5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = sessions.Close(ctx)
	})

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var text strings.Builder
	for part := range sessions.Stream(ctx, map[string]any{
		"command": cmd,
		"env": map[string]any{
			"CLAUDE_TEST_TAG":  "stream-ok",
			"MCP_TOOL_TIMEOUT": "12345",
			"ARIADNE_SECRET":   "must-strip",
		},
	}, sdk.Request{
		Model:    "default",
		Messages: []sdk.Message{{Role: "user", Content: "ping"}},
	}) {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
		if part.Type == "finish" && part.Reason == "error" {
			t.Fatalf("claude stream failed: %s", part.Error)
		}
	}
	want := "tag=stream-ok timeout=12345 tokens=200000 ariadne="
	if text.String() != want {
		t.Fatalf("claude child env = %q, want %q", text.String(), want)
	}
}
