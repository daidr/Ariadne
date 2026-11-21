package server

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"github.com/daidr/Ariadne/internal/fsutil"
)

func (api *API) registerData() {
	api.add("GET", "/api/settings/data", func(r *http.Request) (any, int, error) {
		store := api.options.Data
		if store == nil {
			return nil, 0, badInput("data")
		}
		switch r.URL.Query().Get("sizes") {
		case "0":
			summary, err := store.Summary()
			return summary, 200, err
		case "cached":
			return object{"storage": store.LatestStorage()}, 200, nil
		case "storage":
			return api.measureStorage(r)
		case "reclaimable":
			return api.measureReclaimable(r)
		}
		usage, err := store.Usage(r.Context())
		return usage, 200, err
	})
	api.add("POST", "/api/settings/data/backups", func(r *http.Request) (any, int, error) {
		if api.options.Data == nil {
			return nil, 0, badInput("data")
		}
		now := time.Now()
		if api.options.Now != nil {
			now = api.options.Now()
		}
		backup, err := api.options.Data.Backup(r.Context(), false, now)
		return object{"backup": backup}, 201, err
	})
	api.add("DELETE", "/api/settings/data/backups/:name", func(r *http.Request) (any, int, error) {
		if api.options.Data == nil {
			return nil, 0, badInput("data")
		}
		return nil, 204, api.options.Data.RemoveBackup(r.PathValue("name"))
	})
}

func (api *API) measureReclaimable(r *http.Request) (any, int, error) {
	manager := api.options.Workspaces
	path := r.URL.Query().Get("path")
	if manager == nil || !filepath.IsAbs(path) {
		return nil, 0, badInput("path")
	}
	projects, err := manager.ListWorktrees(r.Context(), false)
	if err != nil {
		return nil, 0, err
	}
	for _, project := range projects {
		for _, tree := range project.Worktrees {
			if tree.Path == path {
				ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
				defer cancel()
				return object{"exclusive": fsutil.MeasureReFSExclusive(ctx, tree.Path)}, 200, nil
			}
		}
	}
	return nil, 0, badInput("path")
}

func (api *API) measureStorage(r *http.Request) (any, int, error) {
	var paths []string
	if manager := api.options.Workspaces; manager != nil {
		projects, err := manager.ListWorktrees(r.Context(), false)
		if err != nil {
			return nil, 0, err
		}
		for _, project := range projects {
			for _, tree := range project.Worktrees {
				if filepath.IsAbs(tree.Path) {
					paths = append(paths, tree.Path)
				}
			}
		}
	}
	snapshot, err := api.options.Data.MeasureStorage(r.Context(), paths)
	return object{"storage": snapshot}, 200, err
}
