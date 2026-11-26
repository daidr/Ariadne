package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/agentruntime"
	"github.com/daidr/Ariadne/sdk"
)

func TestACPEnvChild(t *testing.T) {
	if !slices.Contains(os.Args, "--acp-env-child") {
		return
	}
	tag := os.Getenv("ACP_TEST_TAG")
	send := func(message any) {
		data, _ := json.Marshal(message)
		fmt.Fprintln(os.Stdout, string(data))
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var message struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.ID == nil {
			continue
		}
		switch message.Method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{
				"protocolVersion": 1,
				"agentInfo":       map[string]any{"name": "fake-acp", "version": "1.0"},
			}})
		case "session/new":
			send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{
				"sessionId": "s-1",
				"models": map[string]any{
					"currentModelId": "acp-" + tag,
					"availableModels": []map[string]any{
						{"modelId": "acp-" + tag, "name": "ACP " + tag},
					},
				},
			}})
		case "session/prompt":
			send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": "s-1",
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": "acp-env=" + tag + " ariadne=" + os.Getenv("ARIADNE_SECRET")},
				},
			}})
			send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"stopReason": "end_turn"}})
		}
	}
	os.Exit(0)
}

func acpEnvChildCommand(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`"%s" -test.run=^TestACPEnvChild$ -- --acp-env-child`, exe)
}

func TestACPCustomEnvPropagation(t *testing.T) {
	t.Parallel()
	cmd := acpEnvChildCommand(t)

	listed, err := ListModels(t.Context(), Settings{
		Command: cmd,
		Env:     map[string]string{"ACP_TEST_TAG": "list-ok"},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range listed {
		if m.ID == "acp-list-ok" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListModels missing env-tagged model: %+v", listed)
	}

	sessions := newTestSessions(t, Kind(nil, nil), agentruntime.SessionOptions{Quiet: 5 * time.Millisecond})
	parts := collectParts(t, sessions, map[string]any{
		"command": cmd,
		"env": map[string]any{
			"ACP_TEST_TAG":   "stream-ok",
			"ARIADNE_SECRET": "must-strip",
		},
	}, sdk.Request{
		Model:    "default",
		Messages: []sdk.Message{{Role: "user", Content: "hi"}},
	})
	lastPart(t, parts, "stop")
	if got := textParts(parts); got != "acp-env=stream-ok ariadne=" {
		t.Fatalf("acp child env = %q, want %q", got, "acp-env=stream-ok ariadne=")
	}
}
