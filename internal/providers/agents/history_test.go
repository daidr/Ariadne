package agents

import (
	"strings"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestHistoryAndNativeSessionFingerprints(t *testing.T) {
	type input struct {
		Messages []sdk.Message
		Prefix   *string
		System   string
	}
	image := sdk.Image{MIME: "image/png", Data: "AAAA"}
	messages := []sdk.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "原文 <tag> & \u2028 \u2029 \\u2028 \\u2029", Images: []sdk.Image{image}},
		{Role: "assistant", Content: "回答", ToolCalls: []sdk.ToolCall{{ID: "call_a", Name: "read", Arguments: `{"path":"<file>&"}`}}},
		{Role: "tool", ToolCallID: "call_a", Content: "结果", Images: []sdk.Image{image}, IsError: true},
		{Role: "user", Content: strings.Repeat("a", 399) + "😀"},
	}
	prefix := "新增指令"
	cases := []input{
		{Messages: []sdk.Message{}, System: ""},
		{Messages: []sdk.Message{{Role: "user", Content: "first"}}, System: "sys"},
		{Messages: messages, Prefix: &prefix, System: "新指令\n\u2028"},
		{Messages: messages[:4], System: "system"},
	}
	for _, message := range []sdk.Message{{Role: "assistant", Content: ""}, {Role: "user", Content: ""}, {Role: "tool", ToolCallID: "x", Content: ""}} {
		cases = append(cases, input{Messages: []sdk.Message{message}})
	}
	for _, test := range cases {
		for _, message := range test.Messages {
			_ = Fingerprint(message)
			_ = NativeFingerprint(message)
		}
		_ = RenderHistory(test.Messages)
		_ = JoinInput(test.Messages, test.Prefix)
		_ = SystemHash(test.System)
		_ = UpdatedInstructions(test.System)
	}
	// A changed result must replace Codex's native history even though the
	// generic agent fingerprint still identifies the same tool call.
	before := sdk.Message{Role: "tool", ToolCallID: "call", Content: "old"}
	after := before
	after.Content = "new"
	if Fingerprint(before) != Fingerprint(after) || NativeFingerprint(before) == NativeFingerprint(after) {
		t.Fatal("native history change was not detected")
	}
}
