package codex

import (
	"bufio"
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

func TestCodexEnvChild(t *testing.T) {
	if !slices.Contains(os.Args, "--codex-env-child") {
		return
	}
	tag := os.Getenv("CODEX_TEST_TAG")
	if slices.Contains(os.Args, "debug") {
		fmt.Fprintf(os.Stdout, `{"models":[{"slug":"catalog-%s","tool_mode":"direct"}]}`, tag)
		fmt.Fprintln(os.Stdout)
		os.Exit(0)
	}
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
			send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"userAgent": "codex/0.167.1"}})
		case "model/list":
			send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{
				"data": []map[string]any{{"id": "listed-" + tag, "model": "listed-" + tag}},
			}})
		case "config/read":
			send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"config": map[string]any{}}})
		case "thread/start":
			send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"thread": map[string]any{"id": "th-1"}}})
		case "turn/start":
			send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"turn": map[string]any{"id": "turn-1"}}})
			send(map[string]any{"jsonrpc": "2.0", "method": "item/agentMessage/delta", "params": map[string]any{
				"itemId": "i1",
				"delta":  "codex-env=" + tag + " ariadne=" + os.Getenv("ARIADNE_SECRET"),
			}})
			send(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{
				"turn": map[string]any{"status": "completed"},
			}})
		}
	}
	os.Exit(0)
}

func codexEnvChildCommand(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`"%s" -test.run=^TestCodexEnvChild$ -- --codex-env-child`, exe)
}

func TestCodexCustomEnvPropagation(t *testing.T) {
	t.Parallel()
	cmd := codexEnvChildCommand(t)

	listed, err := ListModels(t.Context(), Settings{
		Command: cmd,
		Env:     map[string]string{"CODEX_TEST_TAG": "list-ok"},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range listed {
		if m.ID == "listed-list-ok" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListModels missing env-tagged model: %+v", listed)
	}

	sessions := newTestSessions(t, Kind(), agentruntime.SessionOptions{Quiet: 5 * time.Millisecond})
	var text strings.Builder
	for part := range sessions.Stream(t.Context(), map[string]any{
		"command": cmd,
		"env": map[string]any{
			"CODEX_TEST_TAG": "stream-ok",
			"ARIADNE_SECRET": "must-strip",
		},
	}, sdk.Request{
		Model:    "default",
		Messages: []sdk.Message{user("hello")},
	}) {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
		if part.Type == "finish" && part.Reason == "error" {
			t.Fatalf("codex stream failed: %s", part.Error)
		}
	}
	if got := text.String(); got != "codex-env=stream-ok ariadne=" {
		t.Fatalf("codex child env = %q, want %q", got, "codex-env=stream-ok ariadne=")
	}
}
