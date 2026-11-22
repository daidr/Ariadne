package server

import (
	"context"
	"net/http"
	"regexp"
	"runtime"
	"time"

	"github.com/daidr/Ariadne/internal/core"
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
		var known *sdk.BranchRevisions
		query := r.URL.Query()
		if query.Has("head") || query.Has("baseHead") {
			head, baseHead := query.Get("head"), query.Get("baseHead")
			if !commitID.MatchString(head) || !commitID.MatchString(baseHead) {
				return nil, 0, badInput("head or baseHead")
			}
			known = &sdk.BranchRevisions{Head: head, BaseHead: baseHead}
		}
		value, err := manager.ThreadChangesSince(r.Context(), r.PathValue("threadId"), known)
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
	api.add("POST", "/api/threads/:threadId/changes/files", func(r *http.Request) (any, int, error) {
		if manager == nil {
			return nil, 0, domainerr.New("error.noBranch", nil)
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		from, head := text(body, "from"), text(body, "head")
		if !commitID.MatchString(from) {
			return nil, 0, badInput("from")
		}
		if !commitID.MatchString(head) {
			return nil, 0, badInput("head")
		}
		rawFiles, ok := body["files"].([]any)
		if !ok || len(rawFiles) == 0 || len(rawFiles) > 64 {
			return nil, 0, badInput("files")
		}
		requests := make([]sdk.FileVersionRequest, len(rawFiles))
		for i, item := range rawFiles {
			entry, ok := item.(map[string]any)
			if !ok {
				return nil, 0, badInput("files")
			}
			path, _ := entry["path"].(string)
			if path == "" {
				return nil, 0, badInput("path")
			}
			var previous *string
			if rawPrev, exists := entry["previousPath"]; exists && rawPrev != nil {
				prev, ok := rawPrev.(string)
				if !ok {
					return nil, 0, badInput("previousPath")
				}
				if prev != "" {
					previous = &prev
				}
			}
			requests[i] = sdk.FileVersionRequest{From: from, Head: head, Path: path, PreviousPath: previous}
		}
		files, err := manager.ThreadFiles(r.Context(), r.PathValue("threadId"), requests)
		return object{"files": files}, 200, err
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
		head := ""
		if body["expectedHead"] != nil {
			head = text(body, "expectedHead")
			if !commitID.MatchString(head) {
				return nil, 0, badInput("expectedHead")
			}
		}
		value, err := manager.MergeThread(r.Context(), r.PathValue("threadId"), method, text(body, "message"), head)
		return value, 200, err
	})
	api.add("POST", "/api/threads/:threadId/merge/undo", func(r *http.Request) (any, int, error) {
		if manager == nil {
			return nil, 0, domainerr.New("error.merge.unsupported", nil)
		}
		thread, err := manager.UndoMerge(r.Context(), r.PathValue("threadId"))
		return object{"thread": thread}, 200, err
	})
	api.add("GET", "/api/threads/:threadId/pull-request/checks", func(r *http.Request) (any, int, error) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		thread, err := api.options.Repository.GetThread(ctx, r.PathValue("threadId"))
		if err != nil {
			return nil, 0, err
		}
		pullRequest, _ := thread.PullRequest.(map[string]any)
		head, _ := pullRequest["headSha"].(string)
		if manager == nil || head == "" {
			return nil, 0, domainerr.New("error.noRepo", nil)
		}
		repo, err := manager.RepoForThread(ctx, thread)
		if err != nil {
			return nil, 0, err
		}
		if repo.Checks == nil {
			return nil, 0, domainerr.New("error.noRepo", nil)
		}
		checks, err := repo.Checks(ctx, head)
		if checks == nil {
			checks = []sdk.CheckInfo{}
		}
		return object{"headSha": head, "checks": checks}, 200, err
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
		source := r.URL.Query().Get("sourceId")
		if source != "" && !core.ValidFileSourceID(source) {
			return nil, 0, badInput("sourceId")
		}
		if manager != nil {
			if r.URL.Query().Get("orphaned") == "1" {
				removed, err = manager.RemoveOrphanedWorktree(r.Context(), project, thread, source)
			} else {
				removed, err = manager.RemoveSourceWorktree(r.Context(), project, thread, source, "user")
			}
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
		if removed == "orphan-changed" {
			return nil, 0, domainerr.New("error.worktreeOrphanChanged", nil)
		}
		if api.options.Data != nil {
			api.options.Data.InvalidateUsage()
		}
		return nil, 204, nil
	})
}
