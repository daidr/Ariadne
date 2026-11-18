package server

import (
	"net/http"
	"time"
)

func (api *API) registerData() {
	api.add("GET", "/api/settings/data", func(r *http.Request) (any, int, error) {
		if api.options.Data == nil {
			return nil, 0, badInput("data")
		}
		usage, err := api.options.Data.Usage(r.Context())
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
