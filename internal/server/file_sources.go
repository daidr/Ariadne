package server

import (
	"context"
	"maps"
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

func appendSecretNames(secrets object) []string {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
