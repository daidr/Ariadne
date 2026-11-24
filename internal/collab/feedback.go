package collab

import (
	"context"
	"time"
)

// RunFeedback recovers unsent feedback at startup and sends idle edits when the
// page did not send them itself. SQLite holds the pending state across restarts.
func (rooms *Rooms) RunFeedback(ctx context.Context) {
	send := func(cutoff time.Time) {
		after := ""
		for ctx.Err() == nil {
			ids, err := rooms.repo.FeedbackCandidates(ctx, after, cutoff)
			if err != nil {
				if ctx.Err() == nil {
					rooms.report(err)
				}
				return
			}
			if len(ids) == 0 {
				return
			}
			for _, id := range ids {
				if ctx.Err() != nil {
					return
				}
				if _, err := rooms.repo.SendAutoPendingArtifactFeedback(ctx, id, nil); err != nil && ctx.Err() == nil {
					rooms.report(err)
				}
				after = id
			}
		}
	}
	send(time.Now())
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			send(now.Add(-2 * time.Minute))
		}
	}
}
