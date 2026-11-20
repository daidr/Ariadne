package server

import (
	"net/http"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/plugins"
)

func (api *API) registerPlugins() {
	host := api.options.Plugins
	view := func() any {
		records := []plugins.Record{}
		if host != nil {
			records = host.Records()
		}
		return object{"plugins": records}
	}
	api.add("GET", "/api/plugins", func(*http.Request) (any, int, error) {
		return view(), 200, nil
	})
	api.add("GET", "/api/plugins/:pluginId/settings", func(r *http.Request) (any, int, error) {
		id := r.PathValue("pluginId")
		if host == nil {
			return nil, 0, domainerr.NotFound(id)
		}
		view, err := host.Settings(r.Context(), id)
		return view, 200, err
	})
	api.add("PUT", "/api/plugins/:pluginId/settings", func(r *http.Request) (any, int, error) {
		id := r.PathValue("pluginId")
		if host == nil {
			return nil, 0, domainerr.NotFound(id)
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		values, ok := body["values"].(map[string]any)
		if !ok {
			return nil, 0, badInput("values")
		}
		view, err := host.SaveSettings(r.Context(), id, values)
		return view, 200, err
	})
	api.add("POST", "/api/plugins", func(r *http.Request) (any, int, error) {
		if host == nil {
			return nil, 0, badInput("plugins")
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		if err := host.AddPath(r.Context(), api.options.Settings, body["path"]); err != nil {
			return nil, 0, err
		}
		if api.options.Data != nil {
			api.options.Data.InvalidateUsage()
		}
		return view(), 200, nil
	})
	api.add("POST", "/api/plugins/remove", func(r *http.Request) (any, int, error) {
		if host == nil {
			return nil, 0, badInput("plugins")
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		if err := host.RemovePath(r.Context(), api.options.Settings, body["path"]); err != nil {
			return nil, 0, err
		}
		if api.options.Data != nil {
			api.options.Data.InvalidateUsage()
		}
		return view(), 200, nil
	})
}
