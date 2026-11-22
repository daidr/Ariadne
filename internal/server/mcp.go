package server

import "net/http"

func (api *API) registerMCP() {
	api.add("PUT", "/api/projects/:projectId/mcp", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		raw, ok := body["servers"].([]any)
		if !ok {
			return nil, 0, badInput("servers")
		}
		known := map[string]bool{}
		if api.options.Settings != nil {
			servers, err := api.options.Settings.MCPServers()
			if err != nil {
				return nil, 0, err
			}
			for _, server := range servers {
				known[server.ID] = true
			}
		}
		ids := []string{}
		for _, raw := range raw {
			id, ok := raw.(string)
			if !ok || !known[id] {
				return nil, 0, badInput("servers")
			}
			ids = append(ids, id)
		}
		project, err := api.options.Repository.SetProjectMCP(r.Context(), r.PathValue("projectId"), ids)
		return object{"project": project}, 200, err
	})
}
