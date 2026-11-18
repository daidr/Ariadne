package runner

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/files"
	"github.com/daidr/Ariadne/internal/skills"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

func (turn *turn) syncInstructions(ctx context.Context, project core.ProjectView) error {
	if turn.Workspaces == nil || project.Source == nil {
		return nil
	}
	var view sdk.SourceView
	if turn.conversation.Role == "thread" {
		thread, err := turn.Repository.GetThread(ctx, turn.conversation.ID)
		if err != nil {
			return err
		}
		if author, ok := thread.ReviewOf.(string); ok && author != "" {
			thread, err = turn.Repository.GetThread(ctx, author)
			if err != nil {
				return err
			}
		}
		if thread.Workspace != nil && !thread.Workspace.Removed {
			view, _ = turn.Workspaces.ForThread(ctx, project.ID, thread.ID)
		}
	}
	if view == nil {
		view, _ = turn.Workspaces.View(ctx, project.ID)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if view == nil {
		return nil
	}
	const name = "AGENTS.md"
	file := core.InstructionFile{Path: name, Hash: "none"}
	data, err := view.ReadBytes(ctx, name, 64<<10)
	if err == nil && files.IsText(data) {
		body := strings.TrimPrefix(string(data), "\ufeff")
		file.Hash = fmt.Sprintf("%x", sha256.Sum256(data))
		file.Content = new(textutil.HeadUnits(body, 16000))
	} else {
		entries, err := view.List(ctx, ".")
		if err != nil {
			return ctx.Err()
		}
		for _, entry := range entries {
			if strings.EqualFold(entry.Name, name) {
				// A read error on an existing file does not clear earlier instructions.
				return nil
			}
		}
	}
	if _, err := turn.Repository.RecordInstructions(ctx, turn.conversation.ID, turn.id, file); err != nil {
		return err
	}
	if turn.conversation.Role == "thread" {
		set, err := skills.Conversation(ctx, turn.Repository, turn.conversation, turn.Workspaces, turn.SkillsDirs)
		if err != nil {
			return err
		}
		turn.skills = set
	}
	return nil
}
