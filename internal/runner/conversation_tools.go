package runner

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"strings"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/projectsettings"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/google/uuid"
)

func (turn *turn) recentMessages(ctx context.Context, id string, input args) ([]args, error) {
	count := 20
	if raw, given := input["limit"]; given {
		value, err := artifactInteger(raw, "limit")
		if err != nil {
			return nil, err
		}
		count = int(min(value, 100))
	}
	page, err := turn.Repository.PageMessages(ctx, id, core.MessageCursor{Limit: &count})
	if err != nil {
		return nil, err
	}
	result := make([]args, 0, len(page.Messages))
	budget := 64 << 10
	for i := len(page.Messages) - 1; i >= 0 && budget > 0; i-- {
		message := page.Messages[i]
		content := textutil.HeadUnits(message.Content, min(16000, budget))
		budget -= len(content)
		item := args{"id": message.ID, "author": message.Author, "kind": message.Kind, "content": content, "edited": message.EditedAt != nil}
		if message.Ask != nil {
			item["ask"] = message.Ask
		}
		if blocks, _ := message.Blocks.([]any); len(blocks) > 0 {
			item["blocks"] = blocks
		}
		result = append(result, item)
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result, nil
}

func (turn *turn) editMessage(ctx context.Context, input args) (string, error) {
	message := text(input, "messageId")
	content, reason := text(input, "content"), text(input, "reason")
	if message == "" || content == "" || reason == "" {
		return "", errors.New("messageId, content and reason are required")
	}
	_, err := turn.Repository.EditMessage(ctx, message, turn.conversation.Role, turn.conversation.ID, turn.keepMessageImages(ctx, content), reason)
	return "Message edited.", err
}

func (turn *turn) ownThread(ctx context.Context, id string) (core.ThreadView, error) {
	thread, err := turn.Repository.GetThread(ctx, id)
	if err == nil && thread.ProjectID != turn.conversation.ProjectID {
		return core.ThreadView{}, domainerr.NotFound(id)
	}
	return thread, err
}

func (turn *turn) threadManagementTools(coordinator bool) []tool {
	threadInputs := func(properties args, fields ...string) (args, []string) {
		if coordinator {
			properties["threadId"] = field("string")
			fields = append(fields, "threadId")
		}
		return properties, fields
	}
	target := func(input args) string {
		if coordinator {
			return text(input, "threadId")
		}
		return turn.conversation.ID
	}
	renameInput, renameRequired := threadInputs(args{"title": field("string")}, "title")
	asideInput, asideRequired := threadInputs(args{"days": field("integer"), "reason": field("string")}, "days")
	return []tool{
		specification("rename_thread", "Give the thread a short title that reflects its goal, in the user's language.",
			renameInput, renameRequired, func(ctx context.Context, input args) (string, error) {
				thread, err := turn.ownThread(ctx, target(input))
				if err != nil {
					return "", err
				}
				_, err = turn.Repository.RenameThread(ctx, thread.ID, text(input, "title"), turn.conversation.ID)
				return "Renamed.", err
			}),
		specification("set_aside", "Set aside a discussion for 1 to 30 days at the user's request. Set days to zero to take it up again.",
			asideInput, asideRequired, func(ctx context.Context, input args) (string, error) {
				thread, err := turn.ownThread(ctx, target(input))
				if err != nil {
					return "", err
				}
				days, ok := input["days"].(float64)
				if !ok || days != math.Trunc(days) || days < 0 || days > 30 {
					return "", errors.New("days must be a whole number between 0 and 30")
				}
				if days == 0 {
					_, err := turn.Repository.EndAside(ctx, thread.ID, turn.conversation.ID)
					return "Taken up again.", err
				}
				_, err = turn.Repository.SetAside(ctx, thread.ID, int(days), text(input, "reason"), turn.conversation.ID)
				return "Set aside. End your turn now.", err
			}),
		specification("suggest_connector", "Show a card to help the user add a necessary MCP server. Supply only the names of secret fields.",
			args{"text": field("string"), "name": field("string"), "why": field("string"), "transport": field("string"), "command": field("string"),
				"args": arrayOf(field("string")), "url": field("string"), "secrets": arrayOf(field("string"))}, []string{"name", "why", "transport"}, turn.suggestConnector),
	}
}

func (turn *turn) suggestConnector(ctx context.Context, input args) (string, error) {
	block := maps.Clone(input)
	delete(block, "text")
	block["type"] = "connector"
	message, err := turn.Repository.PostMessage(ctx, turn.conversation.ProjectID, core.MessageInput{
		ConversationID: turn.conversation.ID, Author: turn.conversation.Role, Content: text(input, "text"), Blocks: []any{block},
	})
	return reply(args{"messageId": message.ID}, err)
}

func (turn *turn) coordinatorTools() []tool {
	return []tool{
		specification("stop_thread", "Stop a thread and its running command. Give a short reason that the user will see.",
			args{"threadId": field("string"), "reason": field("string")}, []string{"threadId", "reason"}, func(ctx context.Context, input args) (string, error) {
				thread, err := turn.ownThread(ctx, text(input, "threadId"))
				if err != nil {
					return "", err
				}
				_, err = turn.Repository.StopThread(ctx, thread.ID, text(input, "reason"), turn.conversation.ID)
				return "Stopped.", err
			}),
		specification("resume_thread", "Resume a thread stopped by the coordinator. The user controls threads they stopped themselves.",
			args{"threadId": field("string")}, []string{"threadId"}, func(ctx context.Context, input args) (string, error) {
				thread, err := turn.ownThread(ctx, text(input, "threadId"))
				if err != nil {
					return "", err
				}
				_, err = turn.Repository.ResumeThread(ctx, thread.ID, turn.conversation.ID)
				return "Resumed.", err
			}),
		specification("read_thread", "Read a thread's latest messages, checklist and status.",
			args{"threadId": threadIDField(), "limit": field("integer")}, []string{"threadId"}, func(ctx context.Context, input args) (string, error) {
				thread, err := turn.ownThread(ctx, text(input, "threadId"))
				if err != nil {
					return "", err
				}
				todo, err := turn.Repository.GetTodo(ctx, thread.ID)
				if err != nil {
					return "", err
				}
				messages, err := turn.recentMessages(ctx, thread.ID, input)
				return reply(args{"threadId": thread.ID, "title": thread.Title, "stage": thread.Stage, "lastSummary": thread.LastSummary, "todo": todo["items"], "messages": messages}, err)
			}),
		specification("name_project", "Give the project a short name in the user's language.",
			args{"name": field("string")}, []string{"name"}, func(ctx context.Context, input args) (string, error) {
				name := textutil.HeadUnits(strings.Join(strings.Fields(text(input, "name")), " "), 60)
				_, err := turn.Repository.RenameProject(ctx, turn.conversation.ProjectID, name)
				return "Named.", err
			}),
		specification("routine", "List recurring work, or add or remove a routine when the user asks. Days are 0 for Sunday through 6 for Saturday. Use local HH:mm.",
			args{"action": field("string"), "prompt": field("string"), "days": arrayOf(field("integer")), "time": field("string"), "routineId": field("string")}, []string{"action"}, turn.routine),
	}
}

func (turn *turn) routine(ctx context.Context, input args) (string, error) {
	projectID, by := turn.conversation.ProjectID, turn.conversation.ID
	action := text(input, "action")
	if action == "list" {
		project, err := turn.Repository.GetProject(ctx, projectID)
		return reply(project.Settings.Routines, err)
	}
	if !turn.userSpoke {
		return "", errors.New("a request from the user is required to change routines")
	}
	switch action {
	case "add":
		days, ok := input["days"].([]any)
		if !ok || len(days) == 0 || len(days) > 7 {
			return "", errors.New("days must contain weekdays from 0 to 6")
		}
		weekdays := make([]int, 0, len(days))
		for _, day := range days {
			value, ok := day.(float64)
			if !ok || value != math.Trunc(value) || value < 0 || value > 6 {
				return "", errors.New("days must contain weekdays from 0 to 6")
			}
			weekdays = append(weekdays, int(value))
		}
		routine := &projectsettings.Routine{
			ID: "rtn_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12], Prompt: text(input, "prompt"), Days: weekdays, Time: text(input, "time"), Enabled: true,
		}
		project, err := turn.Repository.EditRoutine(ctx, projectID, by, "", routine)
		return reply(project.Settings.Routines, err)
	case "remove":
		project, err := turn.Repository.EditRoutine(ctx, projectID, by, text(input, "routineId"), nil)
		return reply(project.Settings.Routines, err)
	default:
		return "", fmt.Errorf("unknown routine action %q", action)
	}
}
