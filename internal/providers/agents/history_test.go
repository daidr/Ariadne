package agents

import (
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestHistoryAndNativeSessionFingerprintsMatchLegacy(t *testing.T) {
	type input struct {
		Messages []sdk.Message `json:"messages"`
		Prefix   *string       `json:"prefix"`
		System   string        `json:"system"`
	}
	image := sdk.Image{MIME: "image/png", Data: "AAAA"}
	messages := []sdk.Message{{Role: "system", Content: "system"}, {Role: "user", Content: "原文 <tag> & \u2028 \u2029 \\u2028 \\u2029", Images: []sdk.Image{image}}, {Role: "assistant", Content: "回答", ToolCalls: []sdk.ToolCall{{ID: "call_a", Name: "read", Arguments: `{"path":"<file>&"}`}}}, {Role: "tool", ToolCallID: "call_a", Content: "结果", Images: []sdk.Image{image}, IsError: true}, {Role: "user", Content: strings.Repeat("a", 399) + "😀"}}
	prefix := "新增指令"
	cases := []input{{Messages: []sdk.Message{}, System: ""}, {Messages: []sdk.Message{{Role: "user", Content: "first"}}, System: "sys"}, {Messages: messages, Prefix: &prefix, System: "新指令\n\u2028"}, {Messages: messages[:4], System: "system"}}
	for _, message := range []sdk.Message{{Role: "assistant", Content: ""}, {Role: "user", Content: ""}, {Role: "tool", ToolCallID: "x", Content: ""}} {
		cases = append(cases, input{Messages: []sdk.Message{message}})
	}
	native := []map[string]any{}
	for _, test := range cases {
		fps, exact := []string{}, []string{}
		for _, message := range test.Messages {
			fps = append(fps, Fingerprint(message))
			exact = append(exact, NativeFingerprint(message))
		}
		native = append(native, map[string]any{"fps": fps, "native": exact, "render": RenderHistory(test.Messages), "join": JoinInput(test.Messages, test.Prefix), "system": SystemHash(test.System), "updated": UpdatedInstructions(test.System)})
	}
	// A changed result must replace Codex's native history even though the
	// generic agent fingerprint still identifies the same tool call.
	before := sdk.Message{Role: "tool", ToolCallID: "call", Content: "old"}
	after := before
	after.Content = "new"
	if Fingerprint(before) != Fingerprint(after) || NativeFingerprint(before) == NativeFingerprint(after) {
		t.Fatal("native history change was not detected")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		t.Log("Bun absent; native checks ran, legacy comparison skipped")
		return
	}
	data, _ := json.Marshal(cases)
	command := legacytest.Command(t, bun, "packages/plugin-agents/src/testing/go-history-fixture.ts")
	command.Dir = filepath.Join("..", "..", "..")
	command.Stdin = strings.NewReader(string(data))
	want, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy %v\n%s", err, want)
	}
	var expected, actual any
	if err := json.Unmarshal(want, &expected); err != nil {
		t.Fatalf("%v\n%s", err, want)
	}
	got, _ := json.Marshal(native)
	json.Unmarshal(got, &actual)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Go %s\nTS %s", got, want)
	}
}
