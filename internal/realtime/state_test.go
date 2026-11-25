package realtime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/textutil"
)

func TestLiveAndDraftSnapshots(t *testing.T) {
	t.Parallel()
	type action struct {
		Op     string `json:"op"`
		Handle string `json:"handle,omitempty"`
		Text   string `json:"text,omitempty"`
	}
	actions := []action{{"live.start", "one", ""}, {"live.output", "one", "\x1b[32mok\x1b[0m\n10%\r50%\r100%\n"}, {"wait", "", ""}, {"live.start", "two", ""}, {"live.output", "one", "stale"}, {"live.end", "one", ""}, {"live.output", "two", "working"}, {"live.end", "two", ""}, {"wait", "", ""}, {"draft.start", "m1", ""}, {"draft.write", "m1", "开始"}, {"draft.write", "m1", "开始检查代码"}, {"wait", "", ""}, {"draft.posted", "m1", ""}, {"draft.write", "m1", "stale"}, {"draft.start", "m2", ""}, {"draft.write", "m2", "再检查"}, {"draft.withdraw", "m2", ""}, {"draft.start", "m3", ""}, {"draft.write", "m3", "最后"}, {"draft.drop", "", ""}}
	screens := []string{"\x1b[32mok\x1b[0m\n10%\r50%\r100%\ndone\r\n", "a\r\r\nb\r", "\x1b]0;title\x07前\n\x1b[1K后", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\", "中文😀\rnew"}
	hub := NewHub()
	defer hub.Close()
	state := NewState(hub)
	defer state.Close()
	now := func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	live := NewLive(state, LiveOptions{Now: now, Interval: 10 * time.Millisecond})
	defer live.Close()
	drafts := NewDrafts(state, LiveOptions{Now: now, Interval: 10 * time.Millisecond})
	defer drafts.Close()
	liveHandles := map[string]LiveHandle{}
	draftHandles := map[string]DraftHandle{}
	states := []any{}
	for _, action := range actions {
		switch action.Op {
		case "live.start":
			handle, err := live.Start(LiveCommand{ProjectID: "p", ConversationID: "c", Command: action.Handle, CWD: "."})
			if err != nil {
				t.Fatal(err)
			}
			liveHandles[action.Handle] = handle
		case "live.output":
			liveHandles[action.Handle].Output(action.Text)
		case "live.end":
			liveHandles[action.Handle].End()
		case "draft.start":
			handle, err := drafts.Start("p", "c", action.Handle)
			if err != nil {
				t.Fatal(err)
			}
			draftHandles[action.Handle] = handle
		case "draft.write":
			if err := draftHandles[action.Handle].Write(action.Text); err != nil {
				t.Fatal(err)
			}
		case "draft.posted":
			draftHandles[action.Handle].Posted()
		case "draft.withdraw":
			draftHandles[action.Handle].Withdraw()
		case "draft.drop":
			drafts.Drop("c")
		case "wait":
			time.Sleep(35 * time.Millisecond)
		}
		draftValues := []DraftUpdate{}
		for update := range state.Snapshots("") {
			if update.Kind == "draft" {
				value, ok := update.Value.(*DraftUpdate)
				if !ok || value == nil {
					t.Fatalf("expected typed *DraftUpdate, got %#v", update.Value)
				}
				draftValues = append(draftValues, *value)
			}
		}
		states = append(states, map[string]any{"live": live.List(""), "drafts": draftValues})
	}
	processed := []string{}
	for _, screen := range screens {
		processed = append(processed, textutil.ScreenText(screen))
	}
	if len(states) != len(actions) || len(processed) != len(screens) || processed[0] != "ok\n100%\ndone\n" {
		t.Fatalf("unexpected snapshots/screens: states=%d processed=%q", len(states), processed)
	}
}
func TestTransientStateCopiesBoundsAndReleasesSnapshots(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	state := NewState(hub)
	defer state.Close()
	data := json.RawMessage(`{"projectId":"p","conversationId":"c","draft":null}`)
	update := Update{Kind: "draft", ProjectID: "p", ConversationID: "c", Data: data}
	if err := state.Set(update, true); err != nil {
		t.Fatal(err)
	}
	data[0] = 'x'
	for value := range state.Snapshots("p") {
		if !json.Valid(value.Data) {
			t.Fatal("caller mutated retained state")
		}
		value.Data[0] = 'x'
	}
	for value := range state.Snapshots("p") {
		if !json.Valid(value.Data) {
			t.Fatal("snapshot mutated retained state")
		}
	}
	update.Data = json.RawMessage(`{}`)
	update.ConversationID = strings.Repeat("x", 513)
	if err := state.Set(update, true); err == nil {
		t.Fatal("unbounded identity")
	}
	update.ConversationID = "c"
	update.Data = json.RawMessage(`"` + strings.Repeat("x", MaxPendingBytes) + `"`)
	if err := state.Set(update, true); err == nil {
		t.Fatal("unbounded transient frame")
	}
	state.DropProject("p")
	update.Data = json.RawMessage(`"` + strings.Repeat("<", MaxPendingBytes/5) + `"`)
	if err := state.Set(update, true); err == nil {
		t.Fatal("unbounded encoded SSE frame")
	}
	state.mu.Lock()
	retained := len(state.entries) != 0 || len(state.order) != 0 || state.bytes != 0
	state.mu.Unlock()
	if retained {
		t.Fatal("project deletion retained state")
	}
	state.Close()
	update.Data = json.RawMessage(`{}`)
	if err := state.Set(update, true); err == nil {
		t.Fatal("closed state accepted update")
	}
}
func TestDraftDropAndCloseCannotResurrectOldHandlesOrTimers(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	state := NewState(hub)
	defer state.Close()
	drafts := NewDrafts(state, LiveOptions{Interval: time.Millisecond})
	defer drafts.Close()
	one, err := drafts.Start("p", "c", "m1")
	if err != nil {
		t.Fatal(err)
	}
	one.Write("old")
	two, err := drafts.Start("p", "c", "m2")
	if err != nil {
		t.Fatal(err)
	}
	two.Write("new")
	one.Write("stale")
	one.Withdraw()
	for update := range state.Snapshots("p") {
		value, ok := update.Value.(*DraftUpdate)
		if !ok || value == nil || value.Draft == nil || value.Draft.MessageID != "m2" || value.Draft.Text != "new" {
			t.Fatal("stale handle replaced draft")
		}
	}
	two.Write("newest")
	drafts.Close()
	two.Write("late")
	time.Sleep(10 * time.Millisecond)
	for range state.Snapshots("p") {
		t.Fatal("closed draft retained or restored state")
	}
	drafts.mu.Lock()
	retained := drafts.bytes != 0 || len(drafts.entries) != 0
	drafts.mu.Unlock()
	if retained {
		t.Fatal("close retained draft text")
	}
}
func TestDraftRejectsOversizedPendingTextAndPostedSnapshotIsRemoved(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	state := NewState(hub)
	defer state.Close()
	drafts := NewDrafts(state, LiveOptions{Interval: time.Hour})
	defer drafts.Close()
	handle, err := drafts.Start("p", "c", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Write("small"); err != nil {
		t.Fatal(err)
	}
	if err := handle.Write(strings.Repeat("\x00", 100*1024)); err == nil {
		t.Fatal("oversized JSON frame retained")
	}
	handle.Posted()
	for range state.Snapshots("p") {
		t.Fatal("posted draft retained in reconnect snapshot")
	}
	drafts.mu.Lock()
	bytes := drafts.bytes
	drafts.mu.Unlock()
	if bytes != 0 {
		t.Fatal("posted text not released")
	}
}

func TestJSONEscapedLenMatchesEncodingJSON(t *testing.T) {
	samples := []string{
		"",
		"hello world",
		`quotes "and" \backslashes\`,
		"lines\n\r\t\b\f\x00\x1f",
		"html <script>&</script>",
		"中文😀测试\u2028\u2029done",
		string([]byte{'a', 0xff, 'b', 0xfe, 'c'}),
	}
	for _, sample := range samples {
		encoded, err := json.Marshal(sample)
		if err != nil {
			t.Fatal(err)
		}
		want := len(encoded) - 2
		if got := jsonEscapedLen(sample); got != want {
			t.Fatalf("jsonEscapedLen(%q) = %d, want %d (encoded=%s)", sample, got, want, encoded)
		}
	}
}
