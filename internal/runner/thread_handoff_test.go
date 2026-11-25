package runner

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/sdk"
)

func TestCoordinatorUsesReturnedThreadIDsForMessagesAndCards(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	home, err := repo.CreateProject(ctx, new("Handoff"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	agent := &turn{Runner: &Runner{Repository: repo}, conversation: core.ConversationView{
		ID: home.CoordinatorID, ProjectID: home.ID, Role: "coordinator",
	}}
	create := func(title string) string {
		t.Helper()
		result, err := agent.createThread(ctx, args{"title": title, "brief": "Initial brief"})
		var response args
		if err != nil || json.Unmarshal([]byte(result), &response) != nil {
			t.Fatalf("create_thread returned %s, %v", result, err)
		}
		id := text(response, "threadId")
		if id == "" || id == "undefined" {
			t.Fatalf("create_thread did not return a usable threadId: %s", result)
		}
		return id
	}
	first, second := create("Check first path"), create("Check second path")
	if first == second {
		t.Fatal("duplicate thread IDs")
	}
	if _, err := agent.messageThread(ctx, args{"threadId": "undefined", "content": "wrong target"}); err == nil || !strings.Contains(err.Error(), "list_threads") {
		t.Fatalf("invalid relay cannot recover the ID: %v", err)
	}
	if _, err := agent.messageThread(ctx, args{"threadId": second, "content": "Follow-up"}); err != nil {
		t.Fatal(err)
	}
	for id, count := range map[string]int{first: 1, second: 2} {
		messages, err := repo.ListMessages(ctx, id)
		if err != nil || len(messages) != count {
			t.Fatalf("relay targeted the wrong thread: %s has %d messages, %v", id, len(messages), err)
		}
	}
	if _, err := agent.postUI(ctx, args{"text": "Invalid", "blocks": []any{args{"type": "thread"}}}); err == nil || !strings.Contains(err.Error(), "blocks[0].threadId") {
		t.Fatalf("missing card ID lacked a useful error: %v", err)
	}
	if _, err := agent.postUI(ctx, args{"text": "Invalid", "threadIds": []any{"undefined"}}); err == nil || !strings.Contains(err.Error(), "list_threads") {
		t.Fatalf("invalid card ID lacked a useful error: %v", err)
	}
	other, err := repo.CreateProject(ctx, new("Different project"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := repo.CreateThread(ctx, other.ID, core.NewThread{Title: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.postUI(ctx, args{"threadIds": []any{foreign.ID}}); err == nil {
		t.Fatal("post_ui accepted a cross-project ID")
	}
	if _, err := agent.postUI(ctx, args{"text": "Assigned", "threadIds": []any{second, first}}); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.postUI(ctx, args{"blocks": []any{args{"type": "thread", "threadId": first}}}); err != nil {
		t.Fatal(err)
	}
	messages, err := repo.ListMessages(ctx, home.CoordinatorID)
	if err != nil || len(messages) != 2 {
		t.Fatalf("invalid cards may have created a message: %d, %v", len(messages), err)
	}
	blocks, _ := messages[0].Blocks.([]any)
	if len(blocks) != 2 || text(objectField(args{"block": blocks[0]}, "block"), "threadId") != second ||
		text(objectField(args{"block": blocks[1]}, "block"), "threadId") != first {
		t.Fatalf("wrong card targets or order: %v", blocks)
	}
}

func TestCoordinatorCanRecoverFromInvalidThreadReferences(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	project, err := repo.CreateProject(ctx, new("Test handoff"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	const body = "你帮我看看新增图标是什么流程"
	userMessage, err := repo.PostMessage(ctx, project.ID, core.MessageInput{
		ConversationID: project.CoordinatorID, Author: "user", Content: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := repo.StartScheduledTurn(ctx, project.CoordinatorID, "mock")
	if err != nil {
		t.Fatal(err)
	}
	agent := &turn{Runner: &Runner{Repository: repo}, id: turnID, conversation: core.ConversationView{
		ID: project.CoordinatorID, ProjectID: project.ID, Role: "coordinator",
	}}
	tools := agent.tools(ctx, project)
	for _, entry := range tools {
		if entry.spec.Name != "create_thread" && entry.spec.Name != "message_thread" {
			continue
		}
		fields := objectField(entry.spec.Parameters, "properties")
		description := text(objectField(fields, "userMessages"), "description")
		if !strings.Contains(description, "exact id") || !strings.Contains(description, "list_messages") {
			t.Errorf("%s did not explain how to provide message IDs: %q", entry.spec.Name, description)
		}
	}
	call := func(id, name string, input args) args {
		t.Helper()
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		_, err = agent.execute(ctx, tools, []sdk.ToolCall{{ID: id, Name: name, Arguments: string(data)}})
		if err != nil {
			t.Fatal(err)
		}
		transcript, err := repo.ListTranscript(ctx, project.CoordinatorID, core.TranscriptOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range transcript {
			if entry["kind"] == "tool" && entry["toolCallId"] == id {
				return entry
			}
		}
		t.Fatalf("tool result %s was not recorded", id)
		return nil
	}
	checkError := func(entry args, fragments ...string) {
		t.Helper()
		content := text(entry, "content")
		if entry["isError"] != true {
			t.Fatalf("expected an error, got %q", content)
		}
		for _, fragment := range fragments {
			if !strings.Contains(content, fragment) {
				t.Errorf("tool error %q did not contain %q", content, fragment)
			}
		}
	}
	checkError(call("body", "create_thread", args{
		"title": "Icon workflow", "brief": "Inspect the repository", "userMessages": []any{userMessage.ID, body},
	}), "userMessages[1]", "list_messages", "brief")
	checkError(call("shape", "create_thread", args{
		"title": "Icon workflow", "brief": "Inspect the repository", "userMessages": body,
	}), "userMessages", "array", "list_messages")
	checkError(call("node", "create_thread", args{
		"title": "Icon workflow", "brief": "Inspect the repository", "nodeId": "nod_missing",
	}), "nodeId", "nod_missing", "read_plan")
	checkError(call("generic", "read_thread", args{"threadId": "thr_missing"}), "thr_missing")
	if threads, err := repo.ListThreads(ctx, project.ID); err != nil || len(threads) != 0 {
		t.Fatalf("failed create_thread calls left a thread: %v, %v", threads, err)
	}

	listed := call("messages", "list_messages", args{})
	if listed["isError"] == true || !strings.Contains(text(listed, "content"), userMessage.ID) {
		t.Fatalf("list_messages did not return the user's ID: %v", listed)
	}
	created := call("retry", "create_thread", args{
		"title": "Icon workflow", "brief": "Inspect the repository", "userMessages": []any{userMessage.ID},
	})
	var result args
	if created["isError"] == true || json.Unmarshal([]byte(text(created, "content")), &result) != nil {
		t.Fatalf("retry with a real message ID failed: %v", created)
	}
	threadID := text(result, "threadId")
	checkError(call("bad-relay", "message_thread", args{
		"threadId": threadID, "content": "Follow-up", "userMessages": []any{"msg_missing"},
	}), "userMessages[0]", "list_messages", "content")
	messages, err := repo.ListMessages(ctx, threadID)
	if err != nil || len(messages) != 1 {
		t.Fatalf("the failed relay should leave only the initial brief: %v, %v", messages, err)
	}
	cites, _ := messages[0].Cites.([]any)
	if len(cites) != 1 || text(cites[0].(map[string]any), "messageId") != userMessage.ID {
		t.Fatalf("the retried brief lost its original user message: %v", cites)
	}
}
