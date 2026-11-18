package server

import (
	"net/http"

	"github.com/daidr/Ariadne/internal/domainerr"
)

func (api *API) registerBackground() {
	api.add("GET", "/api/threads/:threadId/commands/:commandId", func(request *http.Request) (any, int, error) {
		id := request.PathValue("commandId")
		if api.options.Background != nil {
			if output := api.options.Background.Peek(request.PathValue("threadId"), id, 0); output != nil {
				return output, 200, nil
			}
		}
		return nil, 0, domainerr.NotFound(id)
	})
	api.add("POST", "/api/threads/:threadId/commands/:commandId/stop", func(request *http.Request) (any, int, error) {
		id := request.PathValue("commandId")
		if api.options.Background != nil {
			command, err := api.options.Background.Stop(request.Context(), request.PathValue("threadId"), id)
			if err != nil {
				return nil, 0, err
			}
			if command != nil {
				return object{"command": command}, 200, nil
			}
		}
		return nil, 0, domainerr.NotFound(id)
	})
}
