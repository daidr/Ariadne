package server

import (
	"math"
	"net/http"

	"github.com/daidr/Ariadne/internal/trace"
)

func (api *API) registerTrace() {
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
		value, err := trace.Build(r.Context(), api.options.Repository, r.PathValue("conversationId"), options)
		return value, 200, err
	})
}
