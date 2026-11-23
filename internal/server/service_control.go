package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/service"
)

func (api *API) registerServiceControl() {
	api.add(http.MethodGet, "/api/service/status", func(r *http.Request) (any, int, error) {
		if api.options.ServiceControl == nil {
			return nil, 0, domainerr.NotFound("service")
		}
		return api.options.ServiceControl.Snapshot(r.Context()), http.StatusOK, nil
	})
	api.add(http.MethodPost, "/api/service/drain", func(r *http.Request) (any, int, error) {
		if api.options.ServiceControl == nil {
			return nil, 0, domainerr.NotFound("service")
		}
		var req service.DrainRequest
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			return nil, 0, badInput("body")
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, 0, badInput("body")
			}
		}
		snap, err := api.options.ServiceControl.BeginDrain(r.Context(), req)
		if err != nil {
			switch {
			case errors.Is(err, service.ErrInvalidDrainReq):
				return nil, 0, badInput("drain")
			case errors.Is(err, service.ErrCannotRestart):
				return nil, 0, domainerr.New("updates.error.cannotRestart", nil)
			default:
				return nil, 0, err
			}
		}
		return snap, http.StatusOK, nil
	})
	api.add(http.MethodPost, "/api/service/cancel-drain", func(r *http.Request) (any, int, error) {
		if api.options.ServiceControl == nil {
			return nil, 0, domainerr.NotFound("service")
		}
		return api.options.ServiceControl.CancelDrain(r.Context()), http.StatusOK, nil
	})
	api.add(http.MethodPost, "/api/service/shutdown", func(r *http.Request) (any, int, error) {
		if api.options.ServiceControl == nil {
			return nil, 0, domainerr.NotFound("service")
		}
		var req service.ShutdownRequest
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			return nil, 0, badInput("body")
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, 0, badInput("body")
			}
		}
		if err := api.options.ServiceControl.RequestShutdown(req); err != nil {
			if errors.Is(err, service.ErrBusy) {
				return nil, 0, domainerr.New("service.error.busy", nil)
			}
			return nil, 0, err
		}
		return object{"ok": true}, http.StatusOK, nil
	})
}
