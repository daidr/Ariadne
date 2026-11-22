package server

import (
	"context"
	"errors"
	"time"
)

type projectDeletion struct {
	done chan struct{}
	err  error
}

func (api *API) deleteProjectQueued(ctx context.Context, id string) error {
	if _, err := api.options.Repository.GetProject(ctx, id); err != nil {
		return err
	}
	api.deletionMu.Lock()
	if api.deleteClosed {
		api.deletionMu.Unlock()
		return errors.New("API is closed")
	}
	if api.deleteWaiters >= 1024 {
		api.deletionMu.Unlock()
		return badInput("too many waiting project deletions")
	}
	job := api.deletions[id]
	if job == nil {
		if len(api.deletions) >= 128 {
			api.deletionMu.Unlock()
			return badInput("too many pending project deletions")
		}
		job = &projectDeletion{done: make(chan struct{})}
		api.deletions[id] = job
		api.deleteJobs.Add(1)
		go func() {
			defer api.deleteJobs.Done()
			defer func() {
				if recover() != nil {
					job.err = errors.New("project deletion failed")
				}
				api.deletionMu.Lock()
				delete(api.deletions, id)
				close(job.done)
				api.deletionMu.Unlock()
			}()
			runCtx, cancel := context.WithTimeout(api.deleteLife, 2*time.Minute)
			defer cancel()
			job.err = api.deleteProject(runCtx, id)
		}()
	}
	api.deleteWaiters++
	api.deletionMu.Unlock()
	defer func() { api.deletionMu.Lock(); api.deleteWaiters--; api.deletionMu.Unlock() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-job.done:
		return job.err
	}
}
func (api *API) deleteProject(ctx context.Context, id string) error {
	if api.options.Scheduler != nil {
		release, err := api.options.Scheduler.HoldProject(ctx, id)
		if err != nil {
			return err
		}
		defer release()
	}
	if api.options.Workspaces != nil {
		if err := api.options.Workspaces.DiscardProject(ctx, id); err != nil {
			return err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := api.options.Workspaces.InvalidateProject(cleanup, id); err != nil {
				api.report(err)
			}
		}()
	}
	if err := api.options.Repository.DeleteProject(ctx, id); err != nil {
		return err
	}
	api.syncer.Forget(id)
	api.state.DropProject(id)
	api.rooms.DropProject(id)
	if api.options.OnProjectDeleted != nil {
		api.options.OnProjectDeleted(id)
	}
	if api.options.Settings != nil {
		return api.options.Settings.ForgetProject(id)
	}
	return nil
}
