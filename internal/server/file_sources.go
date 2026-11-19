package server

import (
	"context"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/sources"
)

func (api *API) readFileSourceSet(ctx context.Context, body object, project core.ProjectView) (core.FileSourceSet, object, error) {
	result := core.FileSourceSet{Sources: []core.FileSource{}}
	values, ok := body["sources"].([]any)
	if !ok || len(values) > 9 {
		return result, nil, badInput("sources")
	}
	primary := 0
	if raw, exists := body["primaryIndex"]; exists {
		index, ok := raw.(float64)
		if !ok || index < 0 || index >= float64(len(values)) || index != float64(int(index)) {
			return result, nil, badInput("primaryIndex")
		}
		primary = int(index)
	}
	before := project.WritableSources()
	names := make([]string, 0, len(before.Sources))
	for _, source := range before.Sources {
		names = append(names, source.Name)
	}
	legacy := map[string]core.FileSource{}
	if refs, _ := project.References.([]any); len(refs) > 0 {
		for _, raw := range refs {
			reference, _ := raw.(map[string]any)
			id, name, kind := text(reference, "id"), text(reference, "name"), text(reference, "kind")
			settings, _ := reference["settings"].(map[string]any)
			legacy[id] = core.FileSource{ID: id, Name: name, Kind: kind, Settings: settings}
			names = append(names, name)
		}
	}
	secrets := object{}
	for index, raw := range values {
		input, _ := raw.(map[string]any)
		id := text(input, "id")
		name := ""
		var previous *sources.Definition
		if id != "" {
			known, ok := before.Get(id)
			if !ok {
				known, ok = legacy[id]
			}
			if !ok || !core.ValidFileSourceID(id) {
				return result, nil, badInput("sources[" + strconv.Itoa(index) + "].id")
			}
			name = known.Name
			previous = &sources.Definition{Part: sources.Part{Kind: known.Kind, Settings: known.Settings}}
			if known.Hosting != nil {
				value, err := api.readSourcePart(known.Hosting, "hosting")
				if err != nil {
					return result, nil, err
				}
				previous.Hosting = &value
			}
		} else {
			created, err := core.NewFileSourceID()
			if err != nil {
				return result, nil, err
			}
			id = created
		}
		definition, saved, err := api.readSource(ctx, input, project.ID, previous, sources.FileSourceSecretPrefix(id), true)
		if err != nil {
			return result, nil, err
		}
		if _, existing := before.Get(id); existing && previous != nil && !reflect.DeepEqual(definition.Part, previous.Part) {
			// A different workspace root gets a new identity. Branches pinned to
			// the previous source must never be resolved in the new repository.
			id, err = core.NewFileSourceID()
			if err != nil {
				return result, nil, err
			}
			updated := object{}
			for key, value := range sources.FileSourceSecrets(saved, text(input, "id")) {
				updated[sources.FileSourceSecretPrefix(id)+key] = value
			}
			saved = updated
		}
		if name == "" {
			label := definition.Kind
			if api.options.Sources != nil {
				if summary := api.options.Sources.Summary(definition.Part); summary != nil {
					label = *summary
				}
			}
			name = core.ReferenceName(label, names)
			names = append(names, name)
		}
		source := core.FileSource{ID: id, Name: name, Kind: definition.Kind, Settings: definition.Settings}
		if hosting := definition.Hosting; hosting != nil {
			source.Hosting = object{"kind": hosting.Kind, "settings": hosting.Settings}
		}
		result.Sources = append(result.Sources, source)
		maps.Copy(secrets, saved)
		if index == primary {
			result.PrimaryID = id
		}
	}
	return result, secrets, nil
}

func (api *API) createProjectWithFileSources(ctx context.Context, body object, name *string, message string) (any, int, error) {
	if body["source"] != nil || body["references"] != nil {
		return nil, 0, badInput("sources")
	}
	set, secrets, err := api.readFileSourceSet(ctx, body, core.ProjectView{})
	if err != nil {
		return nil, 0, err
	}
	project, err := api.options.Repository.CreateProject(ctx, name, core.ProjectStart{FileSources: &set, Message: message})
	if err != nil {
		return nil, 0, err
	}
	if api.options.Settings != nil && len(secrets) > 0 {
		if err := api.options.Settings.SaveFileSourceSecrets(project.ID, nil, secrets); err != nil {
			return nil, 0, err
		}
	}
	return object{"project": project}, http.StatusCreated, nil
}

func (api *API) registerFileSources() {
	api.add("GET", "/api/projects/:projectId/sources", func(r *http.Request) (any, int, error) {
		id := r.PathValue("projectId")
		project, err := api.options.Repository.GetProject(r.Context(), id)
		if err != nil {
			return nil, 0, err
		}
		set := project.WritableSources()
		known := map[string][]string{}
		if api.options.Settings != nil {
			all := api.options.Settings.SourceSecrets(id)
			for _, source := range set.Sources {
				known[source.ID] = appendSecretNames(sources.FileSourceSecrets(all, source.ID))
			}
			if refs, _ := project.References.([]any); len(refs) > 0 {
				for _, raw := range refs {
					reference, _ := raw.(map[string]any)
					id := text(reference, "id")
					known[id] = appendSecretNames(sources.FileSourceSecrets(all, id))
				}
			}
		}
		return object{"sources": set.Sources, "primaryId": set.PrimaryID, "secretsSet": known}, http.StatusOK, nil
	})
	api.add("PUT", "/api/projects/:projectId/sources", func(r *http.Request) (any, int, error) {
		ctx, id := r.Context(), r.PathValue("projectId")
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		before, err := api.options.Repository.GetProject(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		set, secrets, err := api.readFileSourceSet(ctx, body, before)
		if err != nil {
			return nil, 0, err
		}
		old := before.WritableSources()
		previousSecrets := object{}
		if api.options.Settings != nil {
			previousSecrets = api.options.Settings.SourceSecrets(id)
		}
		project, err := api.options.Repository.SetProjectFileSources(ctx, id, old, set)
		if err != nil {
			return nil, 0, err
		}
		if api.options.Settings != nil {
			touched := make([]string, 0, len(old.Sources)+len(set.Sources))
			for _, source := range old.Sources {
				touched = append(touched, source.ID)
			}
			for _, source := range set.Sources {
				touched = append(touched, source.ID)
			}
			if len(touched) > 0 {
				if err := api.options.Settings.SaveFileSourceSecrets(id, touched, secrets); err != nil {
					return nil, 0, err
				}
			}
		}
		if api.options.Workspaces != nil {
			for _, previous := range old.Sources {
				current, found := set.Get(previous.ID)
				if !found || !reflect.DeepEqual(previous, current) ||
					!reflect.DeepEqual(sources.FileSourceSecrets(previousSecrets, previous.ID), sources.FileSourceSecrets(secrets, previous.ID)) {
					api.options.Workspaces.InvalidateFileSource(id, previous.ID)
				}
			}
		}
		return object{"project": project}, http.StatusOK, nil
	})
}

func appendSecretNames(secrets object) []string {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
