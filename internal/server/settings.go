package server

import (
	"net/http"

	"github.com/daidr/Ariadne/internal/projectsettings"
)

func (api *API) registerSettings() {
	api.registerSettingsRPC()
	toolSwitches := []string{"fetch", "search", "commands", "research", "issueWrites"}
	api.add("GET", "/api/projects/:projectId/settings", func(r *http.Request) (any, int, error) {
		id := r.PathValue("projectId")
		project, err := api.options.Repository.GetProject(r.Context(), id)
		if err != nil {
			return nil, 0, err
		}
		env := []string{}
		if api.options.Settings != nil {
			env = api.options.Settings.ProjectEnvNames(id)
		}
		return object{"settings": project.Settings, "detected": project.Detected, "env": env, "tools": toolSwitches, "hostedMerge": api.options.Workspaces != nil && api.options.Workspaces.HasRepo(r.Context(), id)}, 200, nil
	})
	api.add("PUT", "/api/projects/:projectId/settings", func(r *http.Request) (any, int, error) {
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		id := r.PathValue("projectId")
		project, err := api.options.Repository.GetProject(r.Context(), id)
		if err != nil {
			return nil, 0, err
		}
		var next any = project.Settings
		if given, exists := body["settings"]; exists {
			next = given
		}
		if _, err := projectsettings.Parse(next, toolSwitches); err != nil {
			return nil, 0, err
		}
		env := []string{}
		if api.options.Settings != nil {
			if given, exists := body["env"]; exists {
				env, err = api.options.Settings.UpdateProjectEnv(id, given)
				if err != nil {
					return nil, 0, err
				}
			} else {
				env = api.options.Settings.ProjectEnvNames(id)
			}
		}
		project, err = api.options.Repository.SetProjectSettings(r.Context(), id, next, toolSwitches)
		if err != nil {
			return nil, 0, err
		}
		return object{"project": project, "settings": project.Settings, "detected": project.Detected, "env": env, "tools": toolSwitches, "hostedMerge": api.options.Workspaces != nil && api.options.Workspaces.HasRepo(r.Context(), id)}, 200, nil
	})
}
