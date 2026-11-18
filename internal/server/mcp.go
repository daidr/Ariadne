package server

import (
	"net/http"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/mcp"
)

func (api *API) mcpSettings() (any, error) {
	servers := []any{}
	if api.options.Settings != nil {
		servers = api.options.Settings.PublicMCP()
	}
	status := []mcp.ServerStatus{}
	if api.options.MCP != nil {
		var err error
		status, err = api.options.MCP.Status()
		if err != nil {
			return nil, err
		}
	}
	return object{"servers": servers, "status": status}, nil
}
func (api *API) registerMCP() {
	api.add("GET", "/api/settings/mcp", func(*http.Request) (any, int, error) { result, err := api.mcpSettings(); return result, 200, err })
	api.add("PUT", "/api/settings/mcp", func(r *http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, badInput("settings")
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		if err := api.options.Settings.UpdateMCP(body); err != nil {
			return nil, 0, err
		}
		if api.options.MCP != nil {
			if err := api.options.MCP.Reconfigure(r.Context()); err != nil {
				return nil, 0, err
			}
		}
		result, err := api.mcpSettings()
		return result, 200, err
	})
	api.add("POST", "/api/settings/mcp/:serverId/check", func(r *http.Request) (any, int, error) {
		id := r.PathValue("serverId")
		if api.options.MCP == nil {
			return nil, 0, domainerr.NotFound(id)
		}
		status, err := api.options.MCP.Check(r.Context(), id)
		if err != nil {
			return nil, 0, err
		}
		if status == nil {
			return nil, 0, domainerr.NotFound(id)
		}
		return object{"status": status}, 200, nil
	})
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
