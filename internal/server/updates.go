package server

import (
	"errors"
	"net/http"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/update"
)

func (api *API) registerUpdates() {
	add := func(method, path string, run func(*http.Request, *update.Service) (any, int, error)) {
		api.add(method, path, func(r *http.Request) (any, int, error) {
			if api.options.Updates == nil {
				return nil, 0, domainerr.NotFound(r.URL.Path)
			}
			return run(r, api.options.Updates)
		})
	}
	add("GET", "/api/updates", func(_ *http.Request, service *update.Service) (any, int, error) {
		return service.View(), http.StatusOK, nil
	})
	add("PUT", "/api/updates", func(r *http.Request, service *update.Service) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		automatic, ok := body["automatic"].(bool)
		value := settings.UpdatePreferences{Channel: text(body, "channel"), Automatic: automatic}
		if !ok || !value.Valid() {
			return nil, 0, badInput("updates")
		}
		view, err := service.Save(value)
		if errors.Is(err, update.ErrBusy) {
			return object{"error": domainerr.New("updates.error.busy", nil)}, http.StatusConflict, nil
		}
		return view, http.StatusOK, err
	})
	add("POST", "/api/updates/check", func(r *http.Request, service *update.Service) (any, int, error) {
		return service.Check(r.Context()), http.StatusOK, nil
	})
	add("POST", "/api/updates/install", func(r *http.Request, service *update.Service) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		version, err := required(body, "version")
		if err != nil {
			return nil, 0, err
		}
		view, err := service.Install(version)
		if errors.Is(err, update.ErrUnavailable) {
			return object{"error": domainerr.New("updates.error.unavailable", nil)}, http.StatusConflict, nil
		}
		return view, http.StatusAccepted, err
	})
}
