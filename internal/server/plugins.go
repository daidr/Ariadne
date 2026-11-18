package server

import (
	"net/http"

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
		return view(), 200, nil
	})
}
