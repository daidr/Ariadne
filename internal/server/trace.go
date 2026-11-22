package server

import (
	"fmt"
	"net/http"
	"time"

	"github.com/daidr/Ariadne/internal/secrets"
	"github.com/daidr/Ariadne/internal/trace"
)

func (api *API) registerTrace() {
	api.addRaw("GET", "/api/conversations/:conversationId/trace/export", api.exportTrace)
}

func (api *API) exportTrace(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("conversationId")
	now := time.Now()
	if api.options.Now != nil {
		now = api.options.Now()
	}
	credentials := []string{api.options.Token}
	if api.options.Settings != nil {
		config := api.options.Settings.Current()
		credentials = append(credentials, secrets.ConfiguredValues(config)...)
		if api.options.Models != nil {
			models, _ := config["models"].(map[string]any)
			providers, _ := models["providers"].([]any)
			for _, entry := range providers {
				provider, _ := entry.(map[string]any)
				kind, _ := provider["kind"].(string)
				if adapter, ok := api.options.Models.Get(kind); ok {
					settings, _ := provider["settings"].(map[string]any)
					for key, field := range adapter.Info.Settings.Properties {
						if value, ok := settings[key].(string); ok && field.Secret && len(value) >= 4 {
							credentials = append(credentials, value)
						}
					}
				}
			}
		}
	}
	writer := &exportWriter{writer: w, start: func() {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "ariadne-trace-"+id+".zip"))
		w.Header().Set("Cache-Control", "no-store")
	}}
	err := trace.Export(r.Context(), api.options.Repository, id, api.options.Version, now, secrets.NewDiagnosticRedactor(credentials), writer)
	if !writer.started {
		return err
	}
	if err != nil {
		if r.Context().Err() == nil {
			api.report(err)
		}
		panic(http.ErrAbortHandler)
	}
	return nil
}
