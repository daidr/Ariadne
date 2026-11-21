package runner

import (
	"context"
	"errors"

	"github.com/daidr/Ariadne/internal/core"
)

func (turn *turn) memoryTools() []tool {
	return []tool{
		specification("remember", "Save a lasting fact or preference the user stated or confirmed directly in this conversation. Pass memoryId to change a project entry. Use scope everywhere for a user preference that applies to every project. Only use this while answering the user directly.",
			args{"text": field("string"), "memoryId": field("string"), "scope": args{"type": "string", "enum": []string{"project", "everywhere"}}}, []string{"text"}, turn.remember),
		specification("forget", "Remove a project memory by memoryId, or a line of instructions for every project by its exact text, when the user asks directly in this conversation.",
			args{"memoryId": field("string"), "text": field("string")}, nil, turn.forget),
	}
}

func (turn *turn) canEditMemory(ctx context.Context) error {
	if turn.conversation.Role == "thread" {
		thread, err := turn.Repository.GetThread(ctx, turn.conversation.ID)
		if err != nil {
			return err
		}
		if reviewOf, ok := thread.ReviewOf.(string); ok && reviewOf != "" {
			return errors.New("review threads cannot change memory")
		}
		if !turn.userSpoke {
			return errors.New("memory changes need the user's own input in this turn")
		}
		return nil
	}
	if !turn.directUserInput {
		if turn.userSpoke {
			return errors.New("memory changes requested in a discussion thread are handled by that thread")
		}
		return errors.New("memory changes need the user's own input in this turn")
	}
	return nil
}

func (turn *turn) remember(ctx context.Context, input args) (string, error) {
	if err := turn.canEditMemory(ctx); err != nil {
		return "", err
	}
	conversation := turn.conversation
	words, memoryID := text(input, "text"), optional(input, "memoryId")
	switch text(input, "scope") {
	case "", "project":
		saved, err := turn.Repository.SaveMemory(ctx, conversation.ProjectID, core.MemoryChange{MemoryID: memoryID,
			Text: words, Author: conversation.ID})
		return "Saved as " + saved.ID + ".", err
	case "everywhere":
		if memoryID != nil {
			return "", errors.New("global instructions are updated by their exact text")
		}
		added, err := turn.Settings.AddInstruction(words)
		if err != nil {
			return "", err
		}
		if !added {
			return "This instruction already exists.", nil
		}
		err = turn.Repository.NoteEverywhere(ctx, conversation.ProjectID, conversation.ID, "saved", words)
		return "Saved for every project. It applies from the next turn.", err
	default:
		return "", errors.New("scope must be project or everywhere")
	}
}

func (turn *turn) forget(ctx context.Context, input args) (string, error) {
	if err := turn.canEditMemory(ctx); err != nil {
		return "", err
	}
	conversation := turn.conversation
	if id := text(input, "memoryId"); id != "" {
		return "Removed.", turn.Repository.RemoveMemory(ctx, conversation.ProjectID, id, conversation.ID, "")
	}
	words := text(input, "text")
	removed, err := turn.Settings.RemoveInstruction(words)
	if err != nil {
		return "", err
	}
	if !removed {
		return "", errors.New("no global instruction has that text")
	}
	err = turn.Repository.NoteEverywhere(ctx, conversation.ProjectID, conversation.ID, "removed", words)
	return "Removed.", err
}
