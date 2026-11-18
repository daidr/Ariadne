package server

import (
	"errors"
	"net/http"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/sdk"
)

func (api *API) registerDirectories() {
	api.add("GET", "/api/directories", func(request *http.Request) (any, int, error) {
		listing, err := local.BrowseLocal(request.Context(), request.URL.Query().Get("path"))
		var absent *sdk.DirectoryNotFound
		if errors.As(err, &absent) {
			return object{"listing": nil}, 200, nil
		}
		if err != nil {
			return nil, 0, domainerr.New("error.browse", object{"detail": err.Error()})
		}
		return object{"listing": listing}, 200, nil
	})
}
