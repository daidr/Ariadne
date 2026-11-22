package agentruntime

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestRenderHistoryKeepsNewestTurnsAndDropsOldestWithConsistentImages(t *testing.T) {
	oldUserImg := sdk.Image{MIME: "image/png", Data: "OLD_USER_IMG"}
	oldToolImg := sdk.Image{MIME: "image/png", Data: "OLD_TOOL_IMG"}
	recentImg := sdk.Image{MIME: "image/png", Data: "RECENT_IMG"}
	currentImg := sdk.Image{MIME: "image/png", Data: "CURRENT_IMG"}

	messages := []sdk.Message{
		{Role: "user", Content: "TURN_00_OLD_QUESTION " + strings.Repeat("旧历史消息😀", 150), Images: []sdk.Image{oldUserImg}},
		{Role: "assistant", Content: "calling search", ToolCalls: []sdk.ToolCall{{ID: "call_0", Name: "search", Arguments: `{"pattern":"old"}`}}},
		{Role: "tool", ToolCallID: "call_0", Content: "OLD_TOOL_OUTPUT " + strings.Repeat("x", 1500), Images: []sdk.Image{oldToolImg}},
		{Role: "assistant", Content: "TURN_00_ANSWER " + strings.Repeat("旧回复", 200)},
	}
	for i := 1; i <= 35; i++ {
		userMsg := sdk.Message{Role: "user", Content: fmt.Sprintf("TURN_%02d_USER %s", i, strings.Repeat("对话内容", 250))}
		if i == 35 {
			userMsg.Images = []sdk.Image{recentImg}
		}
		messages = append(messages,
			userMsg,
			sdk.Message{Role: "assistant", Content: fmt.Sprintf("TURN_%02d_ASSISTANT %s", i, strings.Repeat("助手回复", 250))},
		)
	}
	latestContent := "LATEST_TRIGGERING_USER_MESSAGE_msg_01a1ffc8 请只回答这条最新消息"
	messages = append(messages, sdk.Message{Role: "user", Content: latestContent, Images: []sdk.Image{currentImg}})

	out := RenderHistory(messages)
	if !utf8.ValidString(out.Text) {
		t.Fatal("rendered history is not valid UTF-8")
	}
	if len(out.Text) > DefaultHistoryReplayBytes {
		t.Fatalf("rendered history exceeded default budget: %d > %d", len(out.Text), DefaultHistoryReplayBytes)
	}
	if !strings.Contains(out.Text, historyOmittedNote) {
		t.Fatal("expected omission note at start of truncated history")
	}
	if strings.Contains(out.Text, "TURN_00_OLD_QUESTION") || strings.Contains(out.Text, "TURN_01_USER") {
		t.Fatal("oldest turns should have been dropped to keep newest turns")
	}
	if !strings.Contains(out.Text, "TURN_35_USER") || !strings.Contains(out.Text, "TURN_35_ASSISTANT") {
		t.Fatal("most recent turns before current message were not preserved")
	}
	if !strings.Contains(out.Text, "<user>\n"+latestContent+"\n</user>") {
		t.Fatal("triggering latest user message was lost or truncated")
	}
	if !strings.HasSuffix(out.Text, historyFooter+historyAnswerEnding) {
		t.Fatalf("history ending missing or malformed: %q", out.Text[max(0, len(out.Text)-120):])
	}
	// Verify kept turns form a contiguous suffix without holes.
	firstKept := -1
	for i := 1; i <= 35; i++ {
		present := strings.Contains(out.Text, fmt.Sprintf("TURN_%02d_USER", i))
		if present && firstKept == -1 {
			firstKept = i
		} else if !present && firstKept != -1 {
			t.Fatalf("non-contiguous history: turn %d missing after turn %d was kept", i, firstKept)
		}
	}
	// Images must strictly match kept messages (no dropped old images).
	wantImages := []sdk.Image{recentImg, currentImg}
	if !reflect.DeepEqual(out.Images, wantImages) {
		t.Fatalf("images mismatch: got %+v, want %+v", out.Images, wantImages)
	}
}

func TestRenderHistoryCapsOversizedToolResultAndArgs(t *testing.T) {
	hugeSearch := "SEARCH_START\n" + strings.Repeat("match line 中文😀\n", 10_000) + "\nSEARCH_END"
	hugeArgs := `{"path":"big.go","content":"` + strings.Repeat("a", 30_000) + `"}`
	messages := []sdk.Message{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "running search and write", ToolCalls: []sdk.ToolCall{
			{ID: "call_search", Name: "search", Arguments: `{"pattern":"foo"}`},
			{ID: "call_write", Name: "write_file", Arguments: hugeArgs},
		}},
		{Role: "tool", ToolCallID: "call_search", Content: hugeSearch},
		{Role: "tool", ToolCallID: "call_write", Content: "written"},
		{Role: "assistant", Content: "finished earlier task"},
		{Role: "user", Content: "second question"},
		{Role: "assistant", Content: "second answer"},
		{Role: "user", Content: "current question"},
	}

	out := RenderHistory(messages)
	if !utf8.ValidString(out.Text) {
		t.Fatal("rendered history is not valid UTF-8")
	}
	if len(out.Text) > DefaultHistoryReplayBytes {
		t.Fatalf("oversized tool output blew replay budget: %d > %d", len(out.Text), DefaultHistoryReplayBytes)
	}
	if !strings.Contains(out.Text, "SEARCH_START") || !strings.Contains(out.Text, "SEARCH_END") {
		t.Fatal("truncated historical tool result should preserve head and tail")
	}
	if !strings.Contains(out.Text, "bytes from historical tool result") {
		t.Fatal("expected historical tool result truncation marker")
	}
	if !strings.Contains(out.Text, "second question") || !strings.Contains(out.Text, "current question") {
		t.Fatal("subsequent turns or current user message lost because of oversized tool output")
	}
	// Total size should stay small because the 160KB search and 30KB args were capped.
	if len(out.Text) > 12*1024 {
		t.Fatalf("expected capped history to stay compact, got %d bytes", len(out.Text))
	}
}

func TestRenderHistoryAlwaysPreservesTriggeringUserTurnEvenOverBudget(t *testing.T) {
	hugeCurrent := "CURRENT_START " + strings.Repeat("最新消息内容", 12_000) + " CURRENT_END"
	promptUpdate := "<prompt_update>plan updated</prompt_update>"
	oldImg := sdk.Image{MIME: "image/png", Data: "DROPPED"}
	currentImg := sdk.Image{MIME: "image/png", Data: "KEPT"}
	messages := []sdk.Message{
		{Role: "user", Content: "old turn", Images: []sdk.Image{oldImg}},
		{Role: "assistant", Content: "old reply"},
		{Role: "user", Content: hugeCurrent, Images: []sdk.Image{currentImg}},
		{Role: "user", Content: promptUpdate},
	}

	out := RenderHistoryWithBudget(messages, 1024)
	if !strings.Contains(out.Text, historyOmittedNote) {
		t.Fatal("expected omission note when earlier history cannot fit")
	}
	if strings.Contains(out.Text, "old turn") || strings.Contains(out.Text, "old reply") {
		t.Fatal("earlier history should be omitted when current turn already exceeds budget")
	}
	if !strings.Contains(out.Text, "<user>\n"+hugeCurrent+"\n</user>") {
		t.Fatal("current user message must never be truncated")
	}
	if !strings.Contains(out.Text, "<user>\n"+promptUpdate+"\n</user>") {
		t.Fatal("trailing prompt update after latest user message must be preserved")
	}
	if !reflect.DeepEqual(out.Images, []sdk.Image{currentImg}) {
		t.Fatalf("unexpected images: %+v", out.Images)
	}
}

func TestRenderHistoryToolExchangeAtomicityAndMidTurnRecovery(t *testing.T) {
	t.Run("avoids orphaned tool result when newer turns exist", func(t *testing.T) {
		messages := []sdk.Message{
			{Role: "user", Content: "older user"},
			{Role: "assistant", Content: "calling", ToolCalls: []sdk.ToolCall{{ID: "call_1", Name: "read_file", Arguments: `{"path":"a.go"}`}}},
			{Role: "tool", ToolCallID: "call_1", Content: strings.Repeat("r", 1200)},
			{Role: "assistant", Content: "RECENT_ASSISTANT_REPLY"},
			{Role: "user", Content: "CURRENT_USER"},
		}
		// Budget fits omission note + RECENT_ASSISTANT_REPLY + CURRENT_USER,
		// and fits the tool result alone if not grouped, but NOT assistant+tool together.
		out := RenderHistoryWithBudget(messages, 1650)
		if !strings.Contains(out.Text, "RECENT_ASSISTANT_REPLY") || !strings.Contains(out.Text, "CURRENT_USER") {
			t.Fatalf("recent reply and current user should be kept: %s", out.Text)
		}
		if strings.Contains(out.Text, `id="call_1"`) && !strings.Contains(out.Text, `<call id="call_1"`) {
			t.Fatalf("orphaned tool result without matching call: %s", out.Text)
		}
	})

	t.Run("mid-turn recovery ending with tool result preserves latest result", func(t *testing.T) {
		toolImg := sdk.Image{MIME: "image/png", Data: "TOOL_IMG"}
		messages := []sdk.Message{
			{Role: "user", Content: "old " + strings.Repeat("a", 5000)},
			{Role: "assistant", Content: "running", ToolCalls: []sdk.ToolCall{{ID: "call_last", Name: "browser", Arguments: `{}`}}},
			{Role: "tool", ToolCallID: "call_last", Content: "LATEST_TOOL_RESULT", Images: []sdk.Image{toolImg}},
		}
		out := RenderHistoryWithBudget(messages, 400)
		if !strings.Contains(out.Text, `<result id="call_last" name="browser">`) || !strings.Contains(out.Text, "LATEST_TOOL_RESULT") {
			t.Fatalf("mid-turn recovery lost latest tool result or its tool name: %s", out.Text)
		}
		if !strings.HasSuffix(out.Text, historyFooter+historyContinueEnding) {
			t.Fatalf("expected continue ending: %s", out.Text)
		}
		if !reflect.DeepEqual(out.Images, []sdk.Image{toolImg}) {
			t.Fatalf("expected latest tool image to be kept: %+v", out.Images)
		}
	})
}

func TestRenderHistoryKeepsShortHistoryVerbatimWhenWithinBudget(t *testing.T) {
	t.Run("mid-turn 5 KiB tool result is not truncated when total fits budget", func(t *testing.T) {
		fiveKiB := "FILE_HEAD\n" + strings.Repeat("line\n", 1024) + "FILE_TAIL"
		messages := []sdk.Message{
			{Role: "user", Content: "read main.go"},
			{Role: "assistant", ToolCalls: []sdk.ToolCall{{ID: "call_read", Name: "read_file", Arguments: `{"path":"main.go"}`}}},
			{Role: "tool", ToolCallID: "call_read", Content: fiveKiB},
		}
		out := RenderHistory(messages)
		if !strings.Contains(out.Text, fiveKiB) {
			t.Fatal("5 KiB tool result was truncated even though total history fit within budget")
		}
		if strings.Contains(out.Text, "omitted") {
			t.Fatal("unexpected omission notice for history within budget")
		}
	})

	t.Run("8 KiB earlier user message is not truncated when total fits budget", func(t *testing.T) {
		eightKiB := "LOG_HEAD\n" + strings.Repeat("error log line\n", 560) + "LOG_TAIL"
		messages := []sdk.Message{
			{Role: "user", Content: eightKiB},
			{Role: "assistant", Content: "investigating"},
			{Role: "user", Content: "any update?"},
		}
		out := RenderHistory(messages)
		if !strings.Contains(out.Text, eightKiB) {
			t.Fatal("8 KiB user message was truncated even though total history fit within budget")
		}
		if strings.Contains(out.Text, "omitted") {
			t.Fatal("unexpected omission notice for history within budget")
		}
	})
}

func TestRenderHistoryPreservesAllInitialConsecutiveUserMessages(t *testing.T) {
	firstImg := sdk.Image{MIME: "image/png", Data: "FIRST_USER_IMG"}
	firstUser := "INITIAL_USER_QUESTION " + strings.Repeat("详细问题描述", 1200) + " END"
	promptUpdate := "<prompt_update>workspace instructions</prompt_update>"
	messages := []sdk.Message{
		{Role: "user", Content: firstUser, Images: []sdk.Image{firstImg}},
		{Role: "user", Content: promptUpdate},
	}

	out := RenderHistoryWithBudget(messages, 512)
	if !strings.Contains(out.Text, "<user>\n"+firstUser+"\n</user>") {
		t.Fatal("initial triggering user message before prompt_update was truncated or dropped")
	}
	if !strings.Contains(out.Text, "<user>\n"+promptUpdate+"\n</user>") {
		t.Fatal("trailing prompt_update was lost")
	}
	if strings.Contains(out.Text, historyOmittedNote) {
		t.Fatal("omission note should not appear when no messages were dropped")
	}
	if !reflect.DeepEqual(out.Images, []sdk.Image{firstImg}) {
		t.Fatalf("initial user image was lost: %+v", out.Images)
	}
}

func TestRenderHistorySingleFallbackMessageDoesNotEmitOmittedNote(t *testing.T) {
	messages := []sdk.Message{
		{Role: "tool", ToolCallID: "call_only", Content: "ONLY_RESULT_" + strings.Repeat("x", 1000)},
	}
	out := RenderHistoryWithBudget(messages, 300)
	if !strings.Contains(out.Text, "ONLY_RESULT_") {
		t.Fatal("sole fallback message should be preserved")
	}
	if strings.Contains(out.Text, historyOmittedNote) {
		t.Fatalf("omission note must not appear when the only message was kept: %s", out.Text)
	}
}
