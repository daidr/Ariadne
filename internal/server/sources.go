package server

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

func (api *API) sourceKinds() []sdk.ContextProviderInfo {
	if api.options.Sources == nil {
		return []sdk.ContextProviderInfo{}
	}
	return api.options.Sources.Kinds()
}
func (api *API) registerSources() {
	api.add("GET", "/api/projects/:projectId/source", func(request *http.Request) (any, int, error) {
		id := request.PathValue("projectId")
		project, err := api.options.Repository.GetProject(request.Context(), id)
		if err != nil {
			return nil, 0, err
		}
		secrets := []string{}
		if api.options.Settings != nil {
			secrets = api.options.Settings.SourceSecretsSet(id)
		}
		return object{"source": project.Source, "secretsSet": secrets}, 200, nil
	})
	api.add("GET", "/api/source-kinds", func(*http.Request) (any, int, error) { return object{"kinds": api.sourceKinds()}, 200, nil })
	api.add("POST", "/api/source-kinds/:kind/browse", func(request *http.Request) (any, int, error) {
		kind := request.PathValue("kind")
		if api.options.Sources == nil {
			return nil, 0, domainerr.NotFound(kind)
		}
		provider, exists := api.options.Sources.Get(kind)
		if !exists {
			return nil, 0, domainerr.NotFound(kind)
		}
		if provider.Browse == nil {
			return nil, 0, badInput("kind")
		}
		body, err := readJSON(request)
		if err != nil {
			return nil, 0, err
		}
		given, _ := body["settings"].(map[string]any)
		if given == nil {
			given = object{}
		}
		filled := maps.Clone(given)
		if projectID := text(body, "projectId"); projectID != "" && api.options.Settings != nil {
			project, err := api.options.Repository.GetProject(request.Context(), projectID)
			if err != nil {
				return nil, 0, err
			}
			previous := ""
			if source, ok := project.Source.(map[string]any); ok {
				previous, _ = source["kind"].(string)
			}
			plain, secrets := api.options.Settings.SplitSourceSettings(projectID, kind, given, previous, "", sources.SecretFields(provider.Info))
			filled = plain
			maps.Copy(filled, secrets)
		}
		ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
		defer cancel()
		listing, err := provider.Browse(ctx, filled, text(body, "path"))
		var absent *sdk.DirectoryNotFound
		if errors.As(err, &absent) {
			return object{"listing": nil}, 200, nil
		}
		if err != nil {
			var known *domainerr.Error
			if errors.As(err, &known) {
				return nil, 0, err
			}
			return nil, 0, domainerr.New("error.browse", object{"detail": err.Error()})
		}
		return object{"listing": listing}, 200, nil
	})
}
