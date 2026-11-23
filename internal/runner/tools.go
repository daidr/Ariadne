package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type repeatEffect uint8

const (
	repeatTrack repeatEffect = iota
	repeatWait
	repeatProgress
)

type args = map[string]any
type tool struct {
	spec       sdk.Tool
	readOnly   bool
	run        func(context.Context, args) (string, error)
	call       func(context.Context, args) (string, []any, error)
	withCallID func(context.Context, string, args) (string, error)
	poll       func(context.Context, args) (string, repeatEffect, error)
}

func specification(name, description string, fields args, required []string, run func(context.Context, args) (string, error)) tool {
	properties := args{"type": "object", "properties": fields}
	if len(required) != 0 {
		properties["required"] = required
	}
	return tool{spec: sdk.Tool{Name: name, Description: description, Parameters: properties}, run: run}
}

func readOnlySpec(name, description string, fields args, required []string, run func(context.Context, args) (string, error)) tool {
	entry := specification(name, description, fields, required, run)
	entry.readOnly = true
	return entry
}

func (turn *turn) readSkill(ctx context.Context, input args) (string, error) {
	name := strings.TrimPrefix(text(input, "name"), "/")
	entry := turn.skills.Get(name)
	if entry == nil {
		return "", fmt.Errorf("skill %q is unavailable", name)
	}
	if path := text(input, "file"); path != "" {
		data := turn.skills.Read(ctx, name, path)
		if data == nil || data.Binary {
			return "", fmt.Errorf("skill file %q is missing or binary", path)
		}
		return data.Text, nil
	}
	files := turn.skills.Files(ctx, name)
	if len(files.Paths) == 0 {
		return entry.Text, nil
	}
	return entry.Text + "\n\nFiles beside this skill: " + strings.Join(files.Paths, ", "), nil
}

func field(kind string) args { return args{"type": kind} }
func arrayOf(item args) args { return args{"type": "array", "items": item} }

const threadIDHelp = "Use the threadId returned by create_thread or list_threads."
const userMessageIDHelp = "Copy the exact id of a message with author=user from list_messages, or from read_thread for another discussion."

func threadIDField() args {
	return args{"type": "string", "minLength": 1, "description": threadIDHelp}
}

func userMessagesField(textField string) args {
	return args{
		"type":        "array",
		"description": "Optional original user message references, with up to eight distinct IDs in this project. " + userMessageIDHelp + " Put new instructions in " + textField + ".",
		"items":       args{"type": "string", "minLength": 1, "description": userMessageIDHelp},
	}
}

func text(input args, name string) string {
	value, _ := input[name].(string)
	return textutil.Trim(value)
}

func optional(input args, name string) *string {
	if value := text(input, name); value != "" {
		return &value
	}
	return nil
}

func stringsOf(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok || len(values) > 30 {
		return nil, errors.New("expected an array of at most 30 strings")
	}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		item, ok := raw.(string)
		if !ok || item == "" {
			return nil, errors.New("expected nonempty strings")
		}
		out = append(out, item)
	}
	return out, nil
}

func userMessageIDs(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok || len(items) > 30 {
		return nil, errors.New("userMessages must be an array of user message IDs, with at most eight distinct IDs. " + userMessageIDHelp)
	}
	ids := make([]string, 0, len(items))
	for index, item := range items {
		id, ok := item.(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("userMessages[%d] must be a user message ID. %s", index, userMessageIDHelp)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func relayToolError(err error, textField string) error {
	var detail *domainerr.Error
	if !errors.As(err, &detail) {
		return err
	}
	if detail.Key == "error.invalidBody" && detail.Params["reason"] == "cites" {
		return errors.New("userMessages supports up to eight distinct user message IDs. Keep the references needed for this task and retry.")
	}
	if detail.Key != "error.notFound" {
		return err
	}
	switch detail.Field {
	case "cites":
		field := "userMessages"
		if detail.Index != nil {
			field = fmt.Sprintf("userMessages[%d]", *detail.Index)
		}
		return fmt.Errorf("%s does not identify a user message in this project. %s Put instructions in %s. Omit userMessages if you do not need to relay the original message or its attachments.", field, userMessageIDHelp, textField)
	case "nodeId":
		id, _ := detail.Params["what"].(string)
		return fmt.Errorf("nodeId %q is not a plan node in this project. Call read_plan for a current node ID, or omit nodeId to create an unlinked discussion.", textutil.HeadUnits(id, 128))
	case "projectId":
		return errors.New("The current project could not be found. The discussion was not created.")
	}
	return err
}

func toolErrorText(err error) string {
	text := err.Error()
	var missing *domainerr.Error
	if text == "error.notFound" && errors.As(err, &missing) {
		if what, ok := missing.Params["what"].(string); ok && what != "" {
			return fmt.Sprintf("Could not find %q.", textutil.HeadUnits(what, 256))
		}
		return "The requested record could not be found."
	}
	return text
}

func reply(value any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	return string(data), err
}

func (turn *turn) tools(ctx context.Context, project core.ProjectView) []tool {
	conversation := turn.conversation
	specs := []tool{
		specification("send_message", "Send the user a message in this conversation. Use Markdown and the user's language. Attach files by workspace path or existing file ID (up to 50 MiB each).",
			args{"text": field("string"), "replyTo": field("string"), "files": arrayOf(field("string"))}, []string{"text"}, turn.sendMessage),
		turn.readAttachmentTool(),
		turn.updateTodoTool(),
		readOnlySpec("search_messages", "Search messages and thread titles in the current project.",
			args{"query": field("string")}, []string{"query"}, func(ctx context.Context, input args) (string, error) {
				return reply(turn.Repository.Search(ctx, conversation.ProjectID, text(input, "query")))
			}),
		readOnlySpec("list_messages", "Read recent messages of this conversation with their IDs.",
			args{"limit": field("integer")}, nil, func(ctx context.Context, input args) (string, error) {
				return reply(turn.recentMessages(ctx, conversation.ID, input))
			}),
		specification("edit_message", "Correct one of your own messages in this conversation. The user sees the edit reason.",
			args{"messageId": field("string"), "content": field("string"), "reason": field("string")}, []string{"messageId", "content", "reason"}, turn.editMessage),
		specification("react", "Put an emoji reaction on a message of your conversation.",
			args{"messageId": field("string"), "emoji": field("string"), "remove": field("boolean")}, []string{"messageId", "emoji"}, func(ctx context.Context, input args) (string, error) {
				remove, _ := input["remove"].(bool)
				_, err := turn.Repository.React(ctx, text(input, "messageId"), conversation.Role, conversation.ID, text(input, "emoji"), remove)
				return "Reaction updated.", err
			}),
	}
	if turn.skills != nil && len(turn.skills.List()) > 0 {
		specs = append(specs, readOnlySpec("read_skill", "Read a skill or a relative file from its folder.",
			args{"name": field("string"), "file": field("string")}, []string{"name"}, turn.readSkill))
	}
	if conversation.Role == "thread" {
		thread, err := turn.Repository.GetThread(ctx, conversation.ID)
		if err == nil {
			specs = append(specs, turn.reportingTools(thread)...)
			if thread.ReviewOf == nil {
				specs = append(specs, turn.interactionTools()...)
				specs = append(specs, turn.threadManagementTools(false)...)
				specs = append(specs, turn.readPlanTool())
				specs = append(specs, turn.memoryTools()...)
				specs = append(specs, turn.artifactTools()...)
			}
		}
	}
	if conversation.Role == "coordinator" {
		specs = append(specs, turn.interactionTools()...)
		specs = append(specs, turn.planTools()...)
		specs = append(specs, turn.memoryTools()...)
		specs = append(specs, turn.artifactTools()...)
		specs = append(specs, turn.threadManagementTools(true)...)
		specs = append(specs, turn.coordinatorTools()...)
		specs = append(specs,
			specification("create_thread", "Create a discussion thread and send its initial brief. Returns a JSON object with threadId and stage. Wait for this result before using threadId in message_thread, post_ui, or thread links.",
				args{"title": field("string"), "brief": field("string"), "nodeId": args{"type": "string", "description": "Optional existing plan node ID returned by update_plan (ids) or read_plan. If you are adding a new node, wait for update_plan to return its nodeId first."}, "branch": field("string"), "model": field("string"), "userMessages": userMessagesField("brief")}, []string{"title", "brief"}, turn.createThread),
			specification("link_thread_node", "Link an existing discussion thread to a plan node, move it to another node, or unlink it. Pass an existing nodeId from update_plan (ids) or read_plan to link or move, or omit nodeId or set unlink to true to remove the link.",
				args{
					"threadId": threadIDField(),
					"nodeId":   args{"type": "string", "description": "Existing plan node ID returned by update_plan (ids) or read_plan. Omit or leave empty to unlink."},
					"unlink":   args{"type": "boolean", "description": "Set to true to remove the thread's plan node link."},
				}, []string{"threadId"}, turn.linkThreadNode),
			specification("message_thread", "Send instructions or relay the user's messages to a thread of this project.",
				args{"threadId": threadIDField(), "content": field("string"), "userMessages": userMessagesField("content")}, []string{"threadId"}, turn.messageThread),
			readOnlySpec("list_threads", "List discussion threads in the current project with their threadId, title, stage and linked nodeId.",
				args{}, nil, func(ctx context.Context, _ args) (string, error) {
					threads, err := turn.Repository.ListThreads(ctx, conversation.ProjectID)
					var result []args
					for _, thread := range threads[:min(len(threads), 100)] {
						result = append(result, args{"threadId": thread.ID, "title": thread.Title, "stage": thread.Stage, "nodeId": thread.NodeID, "summary": thread.LastSummary})
					}
					return reply(result, err)
				}),
			specification("set_delivery", "Change this project's delivery mode or requirements when the user asks.",
				args{"mode": args{"type": "string", "enum": []string{"merge", "external"}}, "instructions": field("string")}, nil, func(ctx context.Context, input args) (string, error) {
					if !turn.userSpoke {
						return "", errors.New("only change delivery in a turn answering the user's own request")
					}
					project, err := turn.Repository.SetProjectDelivery(ctx, conversation.ProjectID, input)
					return reply(project.Settings.Delivery, err)
				}),
		)
	}
	if turn.Workspaces != nil && turn.Sources != nil && (conversation.Role == "coordinator" || conversation.Role == "thread") {
		if conversation.Role == "coordinator" || turn.canEditThread(ctx) {
			if reference := turn.referenceTool(); reference != nil {
				specs = append(specs, *reference)
			}
			if pending, err := turn.Repository.HasLegacySourceApproval(ctx, conversation.ID); err == nil && pending {
				if legacy := turn.sourceRequestTool(false); legacy != nil {
					specs = append(specs, *legacy)
				}
			}
		}
	}
	if turn.Workspaces != nil && project.Source != nil {
		if tracker, err := turn.Workspaces.Tracker(ctx, project.ID); err == nil && tracker != nil {
			writable := false
			if conversation.Role == "thread" {
				if thread, err := turn.Repository.GetThread(ctx, conversation.ID); err == nil {
					writable = thread.ReviewOf == nil
					if writable {
						specs = append(specs, turn.issueTools(tracker, true)...)
					}
				}
			} else if conversation.Role == "coordinator" {
				specs = append(specs, turn.issueTools(tracker, false)...)
			}
		}
	}
	projectReferences, _ := project.References.([]any)
	ownReferences := false
	if conversation.Role == "thread" && turn.Workspaces != nil {
		if thread, err := turn.Repository.GetThread(ctx, conversation.ID); err == nil {
			references, _ := thread.References.([]any)
			ownReferences = len(references) > 0
		}
	}
	if turn.Workspaces != nil && (project.Source != nil || len(projectReferences) > 0 || ownReferences) {
		specs = append(specs,
			readOnlySpec("list_dir", "List files and directories relative to a source. Pass source to select a named file source.",
				args{"path": field("string"), "source": field("string"), "reference": field("string")}, nil, turn.listDirectory),
			turn.readFileTool(),
			readOnlySpec("search", "Search tracked and unignored files for a regular expression. Results include file paths and matching line numbers. Pass source to select a named file source.",
				args{"path": field("string"), "source": field("string"), "reference": field("string"), "pattern": field("string"), "glob": field("string"), "ignoreCase": field("boolean")}, []string{"pattern"}, turn.searchFiles),
			readOnlySpec("find_files", "Find tracked and unignored files by a glob. A pattern without a slash matches names at any depth. Pass source to select a named file source.",
				args{"path": field("string"), "source": field("string"), "reference": field("string"), "pattern": field("string")}, []string{"pattern"}, turn.findFiles),
		)
		if conversation.Role == "thread" && (project.Source != nil || ownReferences) {
			thread, err := turn.Repository.GetThread(ctx, conversation.ID)
			if err == nil && thread.ReviewOf == nil {
				if repo, err := turn.Workspaces.RepoForThread(ctx, thread); err == nil && repo != nil {
					specs = append(specs, turn.repoTools(repo, project.Settings.Delivery.Mode != "external")...)
				}
				specs = append(specs, specification("write_file", "Create or replace a text file. Pass source to select a writable source.",
					args{"path": field("string"), "content": field("string"), "source": field("string")}, []string{"path", "content"}, turn.writeFile))
				specs = append(specs, turn.saveAttachmentTool())
				specs = append(specs, turn.editorTools()...)
				if !slices.Contains(project.Settings.Tools, "commands") {
					specs = append(specs, turn.commandTools()...)
					specs = append(specs, turn.workspaceNoteTools(ctx, thread.ID)...)
				}
			}
		}
	}
	return specs
}

func (turn *turn) sendMessage(ctx context.Context, input args) (string, error) {
	defer turn.clearDraft()
	content := text(input, "text")
	if content == "" {
		return "", errors.New("text is required")
	}
	ids, err := stringsOf(input["files"])
	if err != nil {
		return "", err
	}
	if len(ids) > 10 {
		return "", errors.New("a message may attach at most 10 files")
	}
	attachments, err := turn.messageAttachments(ctx, ids)
	if err != nil {
		return "", err
	}
	saved, err := turn.sendNativeMessage(ctx, input, content, attachments)
	if err != nil {
		return "", err
	}
	if turn.draft != nil {
		_ = turn.draft.Write(saved.Content)
		turn.draft.Posted()
	}
	return "Sent (" + saved.ID + ").", nil
}

func (turn *turn) updateTodoTool() tool {
	description := "Replace your live checklist. Keep completed items and put next steps in order."
	if turn.conversation.Role == "coordinator" {
		description = "Replace your active work list. Keep only current doing, waiting, and next items in order. Remove completed or merged items on later updates instead of keeping finished history, and pass an empty list when nothing is active."
	}
	return specification("update_todo", description,
		args{"items": arrayOf(args{"type": "object", "properties": args{"label": field("string"), "status": args{"type": "string", "enum": []string{"doing", "next", "waiting", "done"}}, "detail": field("string")}, "required": []string{"label", "status"}})}, []string{"items"}, turn.updateTodo)
}

func (turn *turn) updateTodo(ctx context.Context, input args) (string, error) {
	values, ok := input["items"].([]any)
	if !ok || len(values) > 100 {
		return "", errors.New("items must be an array of at most 100 steps")
	}
	items := make([]core.TodoItem, 0, len(values))
	for _, value := range values {
		item, _ := value.(args)
		status := text(item, "status")
		if status != "doing" && status != "next" && status != "waiting" && status != "done" {
			return "", errors.New("item status must be doing, next, waiting or done")
		}
		label := text(item, "label")
		if label == "" || len(label) > 1024 {
			return "", errors.New("each item needs a short label")
		}
		items = append(items, core.TodoItem{Label: label, Status: status, Detail: optional(item, "detail")})
	}
	_, err := turn.Repository.UpdateTodo(ctx, turn.conversation.ProjectID, turn.conversation.ID, items)
	return "Todo list updated.", err
}

func (turn *turn) createThread(ctx context.Context, input args) (string, error) {
	title, brief := text(input, "title"), text(input, "brief")
	if title == "" || brief == "" {
		return "", errors.New("title and brief are required")
	}
	model := optional(input, "model")
	if model != nil {
		known := false
		var kinds []sdk.ModelProviderInfo
		if turn.Models != nil {
			kinds = turn.Models.Kinds()
		}
		for _, raw := range turn.Settings.ModelChoices(kinds) {
			if choice, ok := raw.(map[string]any); ok && choice["id"] == *model {
				known = true
				break
			}
		}
		if !known {
			return "", errors.New("model must be one of the configured models")
		}
	}
	cites, err := userMessageIDs(input["userMessages"])
	if err != nil {
		return "", err
	}
	thread, err := turn.Repository.CreateBriefedThread(ctx, turn.conversation.ProjectID,
		core.NewThread{Title: title, Model: model, NodeID: optional(input, "nodeId"), Branch: optional(input, "branch")}, brief, cites)
	return reply(args{"threadId": thread.ID, "stage": thread.Stage}, relayToolError(err, "brief"))
}

func (turn *turn) linkThreadNode(ctx context.Context, input args) (string, error) {
	id := text(input, "threadId")
	if id == "" || id == "undefined" || id == "null" {
		return "", errors.New("link_thread_node requires a real threadId. " + threadIDHelp)
	}
	unlink, _ := input["unlink"].(bool)
	var nodeID *string
	if !unlink {
		if raw, present := input["nodeId"]; present && raw != nil {
			value, ok := raw.(string)
			if !ok {
				return "", errors.New("nodeId must be a plan node ID string")
			}
			if clean := textutil.Trim(value); clean != "" {
				nodeID = &clean
			}
		}
	}
	thread, err := turn.Repository.SetThreadNode(ctx, turn.conversation.ProjectID, id, nodeID, turn.conversation.ID)
	if err != nil {
		var detail *domainerr.Error
		if errors.As(err, &detail) {
			switch detail.Key {
			case "error.notFound":
				if detail.Field == "nodeId" {
					what, _ := detail.Params["what"].(string)
					return "", fmt.Errorf("nodeId %q is not a plan node in this project. Call read_plan for a current node ID, or omit nodeId to unlink.", textutil.HeadUnits(what, 128))
				}
				return "", fmt.Errorf("No discussion thread %q in this project. Call list_threads to find its threadId.", id)
			case "error.invalidBody":
				if reason, ok := detail.Params["reason"].(string); ok && reason != "" {
					return "", errors.New(reason)
				}
			}
		}
		return "", err
	}
	return reply(args{"threadId": thread.ID, "nodeId": thread.NodeID}, nil)
}

func (turn *turn) messageThread(ctx context.Context, input args) (string, error) {
	id := text(input, "threadId")
	if id == "" || id == "undefined" || id == "null" {
		return "", errors.New("message_thread requires a real threadId. " + threadIDHelp)
	}
	thread, err := turn.ownThread(ctx, id)
	if err != nil {
		var missing *domainerr.Error
		if errors.As(err, &missing) && missing.Key == "error.notFound" {
			return "", fmt.Errorf("No discussion thread %q in this project. Call list_threads to find its threadId.", id)
		}
		return "", err
	}
	cites, err := userMessageIDs(input["userMessages"])
	if err != nil {
		return "", err
	}
	content := text(input, "content")
	if content == "" && len(cites) == 0 {
		return "", errors.New("message_thread requires instructions in content or original user message IDs in userMessages")
	}
	_, err = turn.Repository.PostMessage(ctx, thread.ProjectID, core.MessageInput{ConversationID: id, Author: "coordinator", Kind: "relay", Content: content, Cites: cites})
	if err != nil {
		return "", relayToolError(err, "content")
	}
	return "Sent.", nil
}

func (turn *turn) source(ctx context.Context, input args) (sdk.SourceView, error) {
	conversation := turn.conversation
	name := text(input, "source")
	if name == "" && text(input, "reference") != "" {
		return turn.Workspaces.ReferenceView(ctx, conversation.ProjectID, text(input, "reference"), conversation.ID)
	}
	var sourceID string
	if name != "" {
		owner := ""
		if conversation.Role == "thread" {
			owner = conversation.ID
		}
		var err error
		sourceID, err = turn.Workspaces.ResolveFileSource(ctx, conversation.ProjectID, name, owner)
		if err != nil {
			return nil, err
		}
		if sourceID == "" {
			return turn.Workspaces.ReferenceView(ctx, conversation.ProjectID, name, conversation.ID)
		}
	}
	if conversation.Role == "thread" {
		owner := conversation.ID
		thread, err := turn.Repository.GetThread(ctx, owner)
		if err != nil {
			return nil, err
		}
		if author, ok := thread.ReviewOf.(string); ok && author != "" {
			original, err := turn.Repository.GetThread(ctx, author)
			if err != nil {
				return nil, err
			}
			if original.Workspace == nil || original.Workspace.Removed {
				return turn.Workspaces.ViewFileSource(ctx, conversation.ProjectID, sourceID)
			}
			if sourceID != "" {
				view, err := turn.Workspaces.ExistingThreadSource(ctx, original, sourceID)
				if err != nil || view != nil {
					return view, err
				}
				return turn.Workspaces.ViewFileSource(ctx, conversation.ProjectID, sourceID)
			}
			owner = author
		}
		view, err := turn.Workspaces.ForThreadSource(ctx, conversation.ProjectID, owner, sourceID)
		if err == nil && thread.Workspace == nil {
			turn.instructionsDirty.Store(true)
		}
		return view, err
	}
	return turn.Workspaces.ViewFileSource(ctx, conversation.ProjectID, sourceID)
}

func (turn *turn) listDirectory(ctx context.Context, input args) (string, error) {
	view, err := turn.source(ctx, input)
	if err != nil {
		return "", err
	}
	entries, err := view.List(ctx, text(input, "path"))
	return reply(entries[:min(len(entries), 1000)], err)
}

func (turn *turn) searchFiles(ctx context.Context, input args) (string, error) {
	view, err := turn.source(ctx, input)
	if err != nil {
		return "", err
	}
	path, pattern := text(input, "path"), text(input, "pattern")
	if pattern == "" {
		return "", errors.New("pattern is required")
	}
	ignore, _ := input["ignoreCase"].(bool)
	options := sdk.SearchOptions{Glob: text(input, "glob"), IgnoreCase: ignore}
	var result sdk.FileSearchResult
	if source, ok := view.(sdk.SourceSearch); ok {
		result, err = source.Search(ctx, pattern, path, options)
	} else {
		result, err = sdk.SearchByReading(ctx, view, pattern, path, options)
	}
	if err != nil {
		return "", err
	}
	if result.Error != nil {
		return "", errors.New(*result.Error)
	}
	if len(result.Lines) == 0 {
		return "No matches.", nil
	}
	output := strings.Join(result.Lines, "\n")
	if result.More {
		output += "\n[More matches exist. Narrow the search to see them.]"
	}
	return output, nil
}

func (turn *turn) findFiles(ctx context.Context, input args) (string, error) {
	view, err := turn.source(ctx, input)
	if err != nil {
		return "", err
	}
	path, pattern := text(input, "path"), text(input, "pattern")
	if pattern == "" {
		return "", errors.New("pattern is required")
	}
	keep, err := sdk.SearchFilter(pattern)
	if err != nil {
		return "", err
	}
	var listed sdk.FileListing
	if source, ok := view.(sdk.SourceFiles); ok {
		listed, err = source.Files(ctx, path)
	} else {
		listed, err = sdk.Walk(ctx, view, path)
	}
	if err != nil {
		return "", err
	}
	found := make([]string, 0, 200)
	for _, path := range listed.Files {
		if keep(path) {
			found = append(found, path)
		}
	}
	slices.Sort(found)
	if len(found) == 0 && !listed.Partial {
		return "No files found.", nil
	}
	output := strings.Join(found[:min(len(found), 200)], "\n")
	if len(found) > 200 {
		output += fmt.Sprintf("\n[%d more. Narrow the pattern.]", len(found)-200)
	}
	if listed.Partial {
		output += "\n[Only part of the directory was searched.]"
	}
	return strings.TrimSpace(output), nil
}
