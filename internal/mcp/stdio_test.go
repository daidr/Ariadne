package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/process"
)

func TestMCPServerChild(t *testing.T) {
	if !slices.Contains(os.Args, "--mcp-child") {
		return
	}
	write := func(value any) { data, _ := json.Marshal(value); fmt.Fprintln(os.Stdout, string(data)) }
	fmt.Fprintln(os.Stdout, "stray log line")
	fmt.Fprintln(os.Stdout, `{"id":1,"result":"not JSON-RPC"}`)
	fmt.Fprintln(os.Stderr, "server stderr")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), MaxMessageBytes)
	for scanner.Scan() {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(2)
		}
		if len(message.ID) == 0 {
			continue
		}
		result := func(value any) { write(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": value}) }
		switch message.Method {
		case "initialize":
			result(map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "native server", "version": "1"}})
		case "tools/list":
			result(map[string]any{"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}})
		case "tools/call":
			switch message.Params.Name {
			case "flood":
				result(map[string]any{"content": []any{map[string]any{"type": "text", "text": strings.Repeat("x", 21<<20)}}})
			case "env":
				name, _ := message.Params.Arguments["name"].(string)
				result(map[string]any{"content": []any{map[string]any{"type": "text", "text": os.Getenv(name)}}})
			default:
				result(map[string]any{"content": []any{map[string]any{"type": "text", "text": "中文"}}, "structuredContent": map[string]any{"answer": 42}, "isError": true})
			}
		}
	}
	os.Exit(0)
}
func nativeMCPCommand(t *testing.T) []string {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{program, "-test.run=^TestMCPServerChild$", "--", "--mcp-child"}
}
func TestNativeMCPStdioProcessEnvAndOversizedLine(t *testing.T) {
	t.Setenv("ARIADNE_TOKEN", "backend private")
	env := process.ThreadEnv(process.Environ())
	env["CUSTOM_TOKEN"] = "configured"
	transport := NewStdio(StdioOptions{Argv: nativeMCPCommand(t), Directory: t.TempDir(), Env: env})
	client, err := NewClient(transport, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	if info, err := client.Connect(t.Context()); err != nil || info.Name != "native server" {
		t.Fatal(info, err)
	}
	if tools, err := client.ListTools(t.Context()); err != nil || len(tools) != 1 {
		t.Fatal(tools, err)
	}
	for _, name := range []string{"ARIADNE_TOKEN", "CUSTOM_TOKEN"} {
		result, err := client.CallTool(t.Context(), "env", map[string]any{"name": name}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		var part struct {
			Text string `json:"text"`
		}
		json.Unmarshal(result.Content[0], &part)
		want := ""
		if name == "CUSTOM_TOKEN" {
			want = "configured"
		}
		if part.Text != want {
			t.Fatal("wrong child environment", name, part.Text)
		}
	}
	result, err := client.CallTool(t.Context(), "echo", nil, time.Second)
	if err != nil || !result.IsError || string(result.StructuredContent) != `{"answer":42}` {
		t.Fatal(result, err)
	}
	if _, err := client.CallTool(t.Context(), "flood", nil, 3*time.Second); err == nil {
		t.Fatal("oversized complete JSON line accepted")
	}
	if err := client.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if transport.StderrTail() != "server stderr" {
		t.Fatal(transport.StderrTail())
	}
}

func TestNativeMCPStdioMatchesLegacyServer(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Skip("Bun absent; native executable tested separately")
	}
	script := legacytest.Path(t, "packages/mcp/src/test-server.ts")
	transport := NewStdio(StdioOptions{Argv: []string{bun, script}, Directory: t.TempDir(), Env: process.ThreadEnv(process.Environ())})
	client, err := NewClient(transport, ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	info, err := client.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	native := map[string]any{"info": info, "tools": tools}
	for _, name := range []string{"echo", "picture", "fail"} {
		result, err := client.CallTool(t.Context(), name, map[string]any{"text": "中文"}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		native[name] = result
	}
	input, _ := json.Marshal(map[string]any{"argv": []string{bun, script}})
	command := legacytest.Command(t, bun, "packages/mcp/src/testing/go-client-fixture.ts")
	command.Dir = filepath.Join("..", "..")
	command.Stdin = strings.NewReader(string(input))
	want, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy %v\n%s", err, want)
	}
	got, _ := json.Marshal(native)
	var actual, expected any
	if json.Unmarshal(got, &actual) != nil || json.Unmarshal(want, &expected) != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Go %s\nTS %s", got, want)
	}
}
