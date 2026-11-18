package server

import (
	"net/http"
	"regexp"
	"runtime"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/sdk"
)

var commitID = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
var threadDirectoryID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (api *API) registerWorkspaces() {
	manager := api.options.Workspaces
	api.add("GET", "/api/threads/:threadId/changes", func(r *http.Request) (any, int, error) {
		if manager == nil {
			return object{"changes": nil, "mergeable": false}, 200, nil
		}
		value, err := manager.ThreadChanges(r.Context(), r.PathValue("threadId"))
		return value, 200, err
	})
	api.add("GET", "/api/threads/:threadId/changes/file", func(r *http.Request) (any, int, error) {
		if manager == nil {
			return nil, 0, domainerr.New("error.noBranch", nil)
		}
		query := r.URL.Query()
		if query.Get("path") == "" {
			return nil, 0, badInput("path")
		}
		for _, name := range []string{"from", "head"} {
			if !commitID.MatchString(query.Get(name)) {
				return nil, 0, badInput(name)
			}
		}
		var previous *string
		if value := query.Get("previousPath"); value != "" {
			previous = &value
		}
		file, err := manager.ThreadFile(r.Context(), r.PathValue("threadId"), sdk.FileVersionRequest{From: query.Get("from"), Head: query.Get("head"), Path: query.Get("path"), PreviousPath: previous})
		return object{"file": file}, 200, err
	})
	api.add("POST", "/api/threads/:threadId/merge", func(r *http.Request) (any, int, error) {
		if manager == nil {
			return nil, 0, domainerr.New("error.merge.unsupported", nil)
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		method := "squash"
		if body["method"] != nil {
			var ok bool
			method, ok = body["method"].(string)
			if !ok {
				return nil, 0, badInput("method")
			}
		}
		if method != "squash" && method != "merge" {
			return nil, 0, badInput("method")
		}
		value, err := manager.MergeThread(r.Context(), r.PathValue("threadId"), method, text(body, "message"))
		return value, 200, err
	})
	api.add("POST", "/api/threads/:threadId/merge/undo", func(r *http.Request) (any, int, error) {
		if manager == nil {
			return nil, 0, domainerr.New("error.merge.unsupported", nil)
		}
		thread, err := manager.UndoMerge(r.Context(), r.PathValue("threadId"))
		return object{"thread": thread}, 200, err
	})
	api.add("POST", "/api/threads/:threadId/pull-request/refresh", func(r *http.Request) (any, int, error) {
		id := r.PathValue("threadId")
		thread, err := api.options.Repository.GetThread(r.Context(), id)
		if err != nil {
			return nil, 0, err
		}
		if manager != nil && manager.HasRepo(r.Context(), thread.ProjectID) {
			if _, err := manager.SyncPullRequest(r.Context(), id, nil, nil); err != nil {
				return nil, 0, err
			}
			thread, err = api.options.Repository.GetThread(r.Context(), id)
		}
		return object{"thread": thread}, 200, err
	})
	api.add("GET", "/api/worktrees", func(r *http.Request) (any, int, error) {
		if manager == nil {
			return object{"projects": []any{}, "windows": runtime.GOOS == "windows"}, 200, nil
		}
		projects, err := manager.ListWorktrees(r.Context(), r.URL.Query().Get("sizes") != "0")
		return object{"projects": projects, "windows": runtime.GOOS == "windows"}, 200, err
	})
	api.add("DELETE", "/api/projects/:projectId/worktrees/:threadId", func(r *http.Request) (any, int, error) {
		project, thread := r.PathValue("projectId"), r.PathValue("threadId")
		if _, err := api.options.Repository.GetProject(r.Context(), project); err != nil {
			return nil, 0, err
		}
		if !threadDirectoryID.MatchString(thread) {
			return nil, 0, badInput("threadId")
		}
		removed := "dirty"
		var err error
		if manager != nil {
			removed, err = manager.RemoveWorktree(r.Context(), project, thread, "user")
			if err != nil {
				return nil, 0, err
			}
		}
		if removed == "busy" {
			return nil, 0, domainerr.New("error.merge.working", nil)
		}
		if removed == "dirty" {
			return nil, 0, domainerr.New("error.worktreeDirty", nil)
		}
		return nil, 204, nil
	})
}
