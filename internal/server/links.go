package server

import (
	"net/http"

	"github.com/daidr/Ariadne/internal/links"
)

func (api *API) registerLinks() {
	api.add("POST", "/api/links", func(request *http.Request) (any, int, error) {
		body, err := readJSON(request)
		if err != nil {
			return nil, 0, err
		}
		values, ok := body["urls"].([]any)
		if !ok || len(values) > links.MaxLinks {
			return nil, 0, badInput("urls")
		}
		addresses := make([]string, 0, len(values))
		for _, value := range values {
			address, ok := value.(string)
			if !ok {
				return nil, 0, badInput("urls")
			}
			addresses = append(addresses, address)
		}
		return object{"links": api.links.Summarize(addresses)}, 200, nil
	})
	api.add("GET", "/api/links/preview", func(request *http.Request) (any, int, error) {
		address := request.URL.Query().Get("url")
		if address == "" {
			return nil, 0, badInput("url")
		}
		return object{"preview": api.links.Preview(request.Context(), address)}, 200, nil
	})
}
