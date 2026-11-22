package server

import (
	"io"
	"net/http"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/files"
)

func (api *API) registerWrites() {
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
