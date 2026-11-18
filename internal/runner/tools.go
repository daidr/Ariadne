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

type args = map[string]any
type tool struct {
	spec       sdk.Tool
	run        func(context.Context, args) (string, error)
	call       func(context.Context, args) (string, []any, error)
	withCallID func(context.Context, string, args) (string, error)
}

func specification(name, description string, fields args, required []string, run func(context.Context, args) (string, error)) tool {
	properties := args{"type": "object", "properties": fields}
	if len(required) != 0 {
		properties["required"] = required
	}
	return tool{spec: sdk.Tool{Name: name, Description: description, Parameters: properties}, run: run}
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
		specification("send_message", "Send the user a message in this conversation. Use Markdown and the user's language.",
			args{"text": field("string"), "replyTo": field("string"), "files": arrayOf(field("string"))}, []string{"text"}, turn.sendMessage),
		specification("update_todo", "Replace your live checklist. Keep completed items and put next steps in order.",
			args{"items": arrayOf(args{"type": "object", "properties": args{"label": field("string"), "status": args{"type": "string", "enum": []string{"doing", "next", "waiting", "done"}}, "detail": field("string")}, "required": []string{"label", "status"}})}, []string{"items"}, turn.updateTodo),
		specification("search_messages", "Search messages and thread titles in the current project.",
			args{"query": field("string")}, []string{"query"}, func(ctx context.Context, input args) (string, error) {
				return reply(turn.Repository.Search(ctx, conversation.ProjectID, text(input, "query")))
			}),
		specification("list_messages", "Read recent messages of this conversation with their IDs.",
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
		specs = append(specs, specification("read_skill", "Read a skill or a relative file from its folder.",
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
			specification("create_thread", "Create a discussion thread and give it a brief. It starts working on the brief in a separate conversation.",
				args{"title": field("string"), "brief": field("string"), "nodeId": field("string"), "branch": field("string"), "model": field("string"), "userMessages": arrayOf(field("string"))}, []string{"title", "brief"}, turn.createThread),
			specification("message_thread", "Send instructions or relay the user's messages to a thread of this project.",
				args{"threadId": field("string"), "content": field("string"), "userMessages": arrayOf(field("string"))}, []string{"threadId"}, turn.messageThread),
			specification("list_threads", "List discussion threads in the current project.",
				args{}, nil, func(ctx context.Context, _ args) (string, error) {
					threads, err := turn.Repository.ListThreads(ctx, conversation.ProjectID)
					var result []args
					for _, thread := range threads[:min(len(threads), 100)] {
						result = append(result, args{"threadId": thread.ID, "title": thread.Title, "stage": thread.Stage, "summary": thread.LastSummary})
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
			specification("list_dir", "List files and directories relative to the project or a named reference.",
				args{"path": field("string"), "source": field("string"), "reference": field("string")}, nil, turn.listDirectory),
			turn.readFileTool(),
			specification("search", "Search tracked and unignored files for a regular expression. Results include file paths and matching line numbers.",
				args{"path": field("string"), "source": field("string"), "reference": field("string"), "pattern": field("string"), "glob": field("string"), "ignoreCase": field("boolean")}, []string{"pattern"}, turn.searchFiles),
			specification("find_files", "Find tracked and unignored files by a glob. A pattern without a slash matches names at any depth.",
				args{"path": field("string"), "source": field("string"), "reference": field("string"), "pattern": field("string")}, []string{"pattern"}, turn.findFiles),
		)
		if conversation.Role == "thread" && project.Source != nil {
			thread, err := turn.Repository.GetThread(ctx, conversation.ID)
			if err == nil && thread.ReviewOf == nil {
				if repo, err := turn.Workspaces.Repo(ctx, project.ID); err == nil && repo != nil {
					specs = append(specs, turn.repoTools(repo)...)
				}
				specs = append(specs, specification("write_file", "Create or replace a text file in your workspace.",
					args{"path": field("string"), "content": field("string")}, []string{"path", "content"}, turn.writeFile))
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
		for _, raw := range turn.Settings.ModelChoices() {
			if choice, ok := raw.(map[string]any); ok && choice["id"] == *model {
				known = true
				break
			}
		}
		if !known {
			return "", errors.New("model must be one of the configured models")
		}
	}
	cites, err := stringsOf(input["userMessages"])
	if err != nil {
		return "", err
	}
	thread, err := turn.Repository.CreateBriefedThread(ctx, turn.conversation.ProjectID,
		core.NewThread{Title: title, Model: model, NodeID: optional(input, "nodeId"), Branch: optional(input, "branch")}, brief, cites)
	return reply(args{"threadId": thread.ID, "stage": thread.Stage}, err)
}

func (turn *turn) messageThread(ctx context.Context, input args) (string, error) {
	id := text(input, "threadId")
	thread, err := turn.Repository.GetThread(ctx, id)
	if err != nil {
		return "", err
	}
	if thread.ProjectID != turn.conversation.ProjectID {
		return "", domainerr.NotFound(id)
	}
	cites, err := stringsOf(input["userMessages"])
	if err != nil {
		return "", err
	}
	_, err = turn.Repository.PostMessage(ctx, thread.ProjectID, core.MessageInput{ConversationID: id, Author: "coordinator", Kind: "relay", Content: text(input, "content"), Cites: cites})
	return "Sent.", err
}

func (turn *turn) source(ctx context.Context, input args) (sdk.SourceView, error) {
	conversation := turn.conversation
	ref := text(input, "source")
	if ref == "" {
		ref = text(input, "reference")
	}
	if ref != "" {
		return turn.Workspaces.ReferenceView(ctx, conversation.ProjectID, ref, conversation.ID)
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
				return turn.Workspaces.View(ctx, conversation.ProjectID)
			}
			owner = author
		}
		view, err := turn.Workspaces.ForThread(ctx, conversation.ProjectID, owner)
		if err == nil && thread.Workspace == nil {
			turn.instructionsDirty = true
		}
		return view, err
	}
	return turn.Workspaces.View(ctx, conversation.ProjectID)
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
