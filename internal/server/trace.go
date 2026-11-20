package server

import (
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/daidr/Ariadne/internal/secrets"
	"github.com/daidr/Ariadne/internal/trace"
)

func (api *API) registerTrace() {
	api.addRaw("GET", "/api/conversations/:conversationId/trace/export", api.exportTrace)
	api.add("GET", "/api/conversations/:conversationId/trace/requests/:seq", func(r *http.Request) (any, int, error) {
		value, err := queryNumber(r.PathValue("seq"))
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 1 || math.Trunc(value) != value || value >= float64(math.MaxInt64) {
			return nil, 0, badInput("seq")
		}
		detail, request, err := trace.RebuildRequest(r.Context(), api.options.Repository, r.PathValue("conversationId"), int64(value))
		if err != nil {
			return nil, 0, err
		}
		var body any
		if detail.Call != nil && request != nil && api.options.Models != nil {
			if provider, ok := api.options.Models.Get(detail.Call.Provider); ok && provider.Describe != nil {
				func() {
					defer func() {
						if recover() != nil {
							body = nil
						}
					}()
					body = provider.Describe(detail.Call.Settings, *request)
				}()
			}
		}
		return object{"request": struct {
			trace.RequestDetail
			Body any `json:"body"`
		}{detail, body}}, 200, nil
	})
	api.add("GET", "/api/conversations/:conversationId/trace", func(r *http.Request) (any, int, error) {
		options := trace.Options{}
		if r.URL.Query().Has("before") {
			value, err := queryNumber(r.URL.Query().Get("before"))
			if err != nil || math.IsInf(value, 0) || math.IsNaN(value) || math.Trunc(value) != value || value >= float64(math.MaxInt64) || value < float64(math.MinInt64) {
				return nil, 0, badInput("before")
			}
			before := int64(value)
			options.Before = &before
		}
		if r.URL.Query().Has("from") {
			value, err := queryNumber(r.URL.Query().Get("from"))
			if err != nil || math.IsNaN(value) || value < 1 || value >= float64(math.MaxInt64) || math.Trunc(value) != value {
				return nil, 0, badInput("from")
			}
			options.From = new(int64(value))
		}
		value, err := trace.Build(r.Context(), api.options.Repository, r.PathValue("conversationId"), options)
		return value, 200, err
	})
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
