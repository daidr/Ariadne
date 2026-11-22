package server

import "net/http"

func (api *API) registerArtifacts() {
	api.addRaw("GET", "/api/artifacts/:artifactId/live", func(w http.ResponseWriter, r *http.Request) error {
		return api.rooms.Serve(w, r, r.PathValue("artifactId"))
	})
}
