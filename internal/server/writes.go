package server

import (
	"io"
	"math"
	"net/http"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/files"
)

func (api *API) registerWrites() {
	repo := api.options.Repository
	api.add("PUT", "/api/projects/:projectId", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		name, err := required(body, "name")
		if err != nil {
			return nil, 0, err
		}
		project, err := repo.RenameProject(r.Context(), r.PathValue("projectId"), name)
		return object{"project": project}, 200, err
	})
	api.add("PUT", "/api/projects/:projectId/slots", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		var slots *int
		if raw, present := body["slots"]; !present {
			return nil, 0, badInput("slots")
		} else if raw != nil {
			value, ok := raw.(float64)
			if !ok || value != math.Trunc(value) || value < 1 || value > 16 {
				return nil, 0, badInput("slots")
			}
			n := int(value)
			slots = &n
		}
		project, err := repo.SetProjectSlots(r.Context(), r.PathValue("projectId"), slots)
		return object{"project": project}, 200, err
	})
	api.add("PUT", "/api/projects/:projectId/autopilot", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		paused, ok := body["paused"].(bool)
		if !ok {
			return nil, 0, badInput("paused")
		}
		project, err := repo.SetAutopilotPaused(r.Context(), r.PathValue("projectId"), paused)
		return object{"project": project}, 200, err
	})
	api.add("POST", "/api/projects/:projectId/files", func(r *http.Request) (any, int, error) {
		forMessage := r.URL.Query().Get("for") == "message"
		limit := files.MaxImageBytes
		if forMessage {
			limit = files.MaxFileBytes
		}
		maxMB := limit / 1024 / 1024
		if r.ContentLength > int64(limit) {
			return nil, 0, domainerr.New("error.fileTooLarge", object{"max": maxMB})
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
		if err != nil {
			return nil, 0, err
		}
		if len(data) > limit {
			return nil, 0, domainerr.New("error.fileTooLarge", object{"max": maxMB})
		}
		file, err := api.files.Save(r.Context(), r.PathValue("projectId"), r.Header.Get("Content-Type"), data, forMessage)
		return object{"file": file}, 201, err
	})
}
