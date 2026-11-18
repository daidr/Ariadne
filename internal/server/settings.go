package server

import (
	"net/http"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/projectsettings"
)

func (api *API) registerSettings() {
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
	searchView := func() any {
		var models []string
		var plugins []any
		if api.options.SearchAvailable != nil {
			models, plugins = api.options.SearchAvailable()
		} else {
			if api.options.Models != nil {
				for _, model := range api.options.Models.SearchModels(api.options.Settings) {
					models = append(models, model.Label)
				}
			}
			if api.options.Plugins != nil {
				for _, search := range api.options.Plugins.SearchProviders() {
					plugins = append(plugins, search.Title)
				}
			}
		}
		return api.options.Settings.PublicSearch(models, plugins)
	}
	api.add("GET", "/api/settings/search", func(*http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, domainerr.NotFound("settings")
		}
		return searchView(), 200, nil
	})
	api.add("PUT", "/api/settings/search", func(r *http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, domainerr.NotFound("settings")
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		if _, err := api.options.Settings.UpdateSearch(body); err != nil {
			return nil, 0, err
		}
		return searchView(), 200, nil
	})
	api.add("POST", "/api/settings/search/check", func(r *http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, domainerr.NotFound("settings")
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		result, err := api.options.Settings.CheckSearch(r.Context(), body, api.options.HTTPClient)
		return result, 200, err
	})
	api.add("GET", "/api/settings/work", func(*http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, badInput("settings")
		}
		return api.options.Settings.Work(), 200, nil
	})
	api.add("PUT", "/api/settings/work", func(r *http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, badInput("settings")
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		value, err := api.options.Settings.UpdateWork(body)
		return value, 200, err
	})
	api.add("GET", "/api/settings/input", func(*http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, domainerr.NotFound("settings")
		}
		return api.options.Settings.Input(), 200, nil
	})
	api.add("PUT", "/api/settings/input", func(r *http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, domainerr.NotFound("settings")
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		value, err := api.options.Settings.UpdateInput(body)
		return value, 200, err
	})
	api.add("GET", "/api/settings/concurrency", func(*http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, domainerr.NotFound("settings")
		}
		return api.options.Settings.Concurrency(), 200, nil
	})
	api.add("PUT", "/api/settings/concurrency", func(r *http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, domainerr.NotFound("settings")
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		value, err := api.options.Settings.UpdateConcurrency(body)
		if err != nil {
			return nil, 0, err
		}
		if api.options.Scheduler != nil {
			if err := api.options.Scheduler.SetSlots(int(value["slots"].(float64)), int(value["reserved"].(float64))); err != nil {
				return nil, 0, err
			}
		}
		return value, 200, nil
	})
}
