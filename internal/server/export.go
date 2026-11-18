package server

import (
	"fmt"
	"net/http"
	"time"
)

type exportWriter struct {
	writer  http.ResponseWriter
	start   func()
	started bool
}

func (writer *exportWriter) Write(data []byte) (int, error) {
	if !writer.started {
		writer.started = true
		writer.start()
	}
	return writer.writer.Write(data)
}
func (api *API) registerExport() {
	api.addRaw("GET", "/api/projects/:projectId/export", func(w http.ResponseWriter, r *http.Request) error {
		id := r.PathValue("projectId")
		now := time.Now()
		if api.options.Now != nil {
			now = api.options.Now()
		}
		writer := &exportWriter{writer: w, start: func() {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "ariadne-"+id+".json"))
			w.Header().Set("Cache-Control", "no-store")
			http.SetCookie(w, api.auth.FileSessionCookie(r))
		}}
		err := api.options.Repository.ExportProject(r.Context(), id, writer, now.UnixMilli())
		if !writer.started {
			return err
		}
		if err != nil && r.Context().Err() == nil {
			api.report(err)
		}
		// An interrupted download is terminated; never append an error envelope
		// to the JSON document after its response has started.
		if err != nil {
			panic(http.ErrAbortHandler)
		}
		return nil
	})
}
