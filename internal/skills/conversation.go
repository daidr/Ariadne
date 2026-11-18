package skills

import (
	"context"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/workspaces"
	"github.com/daidr/Ariadne/sdk"
)

// Conversation never creates a worktree just to discover skills. Reviewers read
// their author's existing workspace; otherwise use the project's source view.
func Conversation(ctx context.Context, repo *core.Repository, conversation core.ConversationView, manager *workspaces.Manager, localDirs []string) (*Set, error) {
	var view sdk.SourceView
	if manager != nil && manager.Available(ctx, conversation.ProjectID) {
		if conversation.Role == "thread" {
			thread, err := repo.GetThread(ctx, conversation.ID)
			if err != nil {
				return nil, err
			}
			if owner, ok := thread.ReviewOf.(string); ok && owner != "" {
				thread, err = repo.GetThread(ctx, owner)
				if err != nil {
					return nil, err
				}
			}
			if thread.Workspace != nil && !thread.Workspace.Removed {
				view, _ = manager.ForThread(ctx, conversation.ProjectID, thread.ID)
			}
		}
		if view == nil && manager.Viewable(ctx, conversation.ProjectID, 2*time.Second) {
			view, _ = manager.View(ctx, conversation.ProjectID)
		}
	}
	set := Load(ctx, view, localDirs)
	return set, ctx.Err()
}
