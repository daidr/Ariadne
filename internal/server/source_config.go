package server

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

func (api *API) readSourcePart(raw any, what string) (sources.Part, error) {
	body, _ := raw.(map[string]any)
	kind, err := required(body, "kind")
	if err != nil {
		return sources.Part{}, err
	}
	var provider sdk.ContextProvider
	if api.options.Sources != nil {
		var exists bool
		provider, exists = api.options.Sources.Get(kind)
		if !exists {
			return sources.Part{}, badInput(what + ".kind")
		}
	}
	settings, _ := body["settings"].(map[string]any)
	trimmed := object{}
	for key, value := range settings {
		if text, ok := value.(string); ok {
			value = textutil.Trim(text)
		}
		trimmed[key] = value
	}
	for _, key := range provider.Info.Settings.Required {
		if field := provider.Info.Settings.Properties[key]; field.Type == "string" && field.Secret {
			continue
		}
		value, exists := trimmed[key]
		if !exists || value == "" {
			prefix := "settings."
			if what != "source" {
				prefix = "hosting.settings."
			}
			return sources.Part{}, badInput(prefix + key)
		}
	}
	return sources.Part{Kind: kind, Settings: trimmed}, nil
}
func (api *API) splitSource(project string, part sources.Part, previous, prefix string) (object, object, error) {
	fields := []string{}
	if api.options.Sources != nil {
		if provider, exists := api.options.Sources.Get(part.Kind); exists {
			fields = sources.SecretFields(provider.Info)
		}
	}
	if api.options.Settings != nil {
		plain, secrets := api.options.Settings.SplitSourceSettings(project, part.Kind, part.Settings, previous, prefix, fields)
		return plain, secrets, nil
	}
	plain := maps.Clone(part.Settings)
	for _, field := range fields {
		if value := plain[field]; value != nil && value != "" {
			return nil, nil, badInput("settings")
		}
		delete(plain, field)
	}
	return plain, object{}, nil
}
func (api *API) readSource(ctx context.Context, raw any, project string, previous *sources.Definition, secretPrefix string, writable bool) (sources.Definition, object, error) {
	part, err := api.readSourcePart(raw, "source")
	if err != nil {
		return sources.Definition{}, nil, err
	}
	body, _ := raw.(map[string]any)
	var hosting *sources.Part
	if body["hosting"] != nil {
		value, err := api.readSourcePart(body["hosting"], "hosting")
		if err != nil {
			return sources.Definition{}, nil, err
		}
		hosting = &value
	}
	if hosting != nil && api.options.Sources != nil {
		host, _ := api.options.Sources.Get(hosting.Kind)
		own, _ := api.options.Sources.Get(part.Kind)
		if !host.Info.Hosting {
			return sources.Definition{}, nil, badInput("hosting.kind")
		}
		if !own.Info.Remote {
			return sources.Definition{}, nil, badInput("hosting")
		}
	}
	before := ""
	beforeHost := ""
	if previous != nil {
		before = previous.Kind
		if previous.Hosting != nil {
			beforeHost = previous.Hosting.Kind
		}
	}
	if api.options.Sources != nil {
		var oldSource, oldHosting *sources.Part
		if previous != nil {
			oldSource, oldHosting = &previous.Part, previous.Hosting
		}
		if err := api.prepareSource(&part, oldSource); err != nil {
			return sources.Definition{}, nil, err
		}
		if hosting != nil {
			if err := api.prepareSource(hosting, oldHosting); err != nil {
				return sources.Definition{}, nil, err
			}
		}
	}
	plain, secrets, err := api.splitSource(project, part, before, secretPrefix)
	if err != nil {
		return sources.Definition{}, nil, err
	}
	source := sources.Definition{Part: sources.Part{Kind: part.Kind, Settings: plain}}
	if hosting != nil {
		plain, hostSecrets, err := api.splitSource(project, *hosting, beforeHost, secretPrefix+sources.HostingSecret)
		if err != nil {
			return sources.Definition{}, nil, err
		}
		source.Hosting = &sources.Part{Kind: hosting.Kind, Settings: plain}
		maps.Copy(secrets, hostSecrets)
	}
	if api.options.Sources != nil {
		connectionSecrets := make(object, len(secrets))
		for key, value := range secrets {
			connectionSecrets[strings.TrimPrefix(key, secretPrefix)] = value
		}
		caps, err := api.options.Sources.ConnectSource(ctx, source, connectionSecrets)
		if err != nil {
			return sources.Definition{}, nil, err
		}
		if caps.Close != nil {
			if err := caps.Close(); err != nil {
				return sources.Definition{}, nil, err
			}
		}
		if writable && (caps.Workspace == nil || caps.Workspace.Open == nil || caps.Workspace.ViewOnly) {
			return sources.Definition{}, nil, badInput("source does not provide a writable workspace")
		}
	}
	return source, secrets, nil
}

func (api *API) prepareSource(part *sources.Part, previous *sources.Part) error {
	provider, ok := api.options.Sources.Get(part.Kind)
	if !ok {
		return badInput("kind")
	}
	var last object
	if previous == nil || previous.Kind != part.Kind {
		for key, value := range sources.Defaults(provider) {
			if _, supplied := part.Settings[key]; !supplied {
				part.Settings[key] = value
			}
		}
	} else {
		last = previous.Settings
	}
	for key, field := range provider.Info.Settings.Properties {
		if field.Directory && part.Settings[key] == "" && !slices.Contains(provider.Info.Settings.Required, key) {
			delete(part.Settings, key)
		}
	}
	if provider.ValidateSettings != nil {
		return provider.ValidateSettings(part.Settings, last)
	}
	return nil
}

type checkedReference struct {
	part    sources.Part
	secrets object
	where   string
}

func sourceObject(source sources.Definition) (object, error) {
	data, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	var result object
	err = json.Unmarshal(data, &result)
	return result, err
}

func (api *API) checkReference(ctx context.Context, project string, source *sources.Definition, sourceSecrets object, part sources.Part) (checkedReference, error) {
	plain, secrets, err := api.splitSource(project, part, "", "")
	if err != nil {
		return checkedReference{}, err
	}
	reference := sources.Part{Kind: part.Kind, Settings: plain}
	where := part.Kind
	if registry := api.options.Sources; registry != nil {
		borrowed := registry.ReferenceSecrets(source, sources.Reference{Part: reference}, sourceSecrets)
		maps.Copy(borrowed, secrets)
		connected, err := registry.ConnectReference(ctx, reference, borrowed)
		if err != nil {
			return checkedReference{}, err
		}
		defer connected.Close()
		view, err := connected.View(ctx)
		if err == nil {
			_, err = view.List(ctx, ".")
		}
		if err != nil {
			var known *domainerr.Error
			if errors.As(err, &known) {
				return checkedReference{}, err
			}
			return checkedReference{}, domainerr.New("error.workspace", object{"detail": err.Error()})
		}
		if summary := registry.Summary(reference); summary != nil {
			where = *summary
		}
	}
	return checkedReference{part: reference, secrets: secrets, where: where}, nil
}
func (api *API) registerSourceConfig() {
	api.registerFileSources()
	api.add("POST", "/api/projects", func(request *http.Request) (any, int, error) {
		body, err := readJSON(request)
		if err != nil {
			return nil, 0, err
		}
		var name *string
		if value, ok := body["name"].(string); ok {
			name = &value
		}
		message := text(body, "message")
		if _, list := body["sources"]; list {
			return api.createProjectWithFileSources(request.Context(), body, name, message)
		}
		rawReferences := []any{}
		if body["references"] != nil {
			var ok bool
			rawReferences, ok = body["references"].([]any)
			if !ok {
				return nil, 0, badInput("references")
			}
		}
		var source *sources.Definition
		secrets := object{}
		if body["source"] != nil {
			value, readSecrets, err := api.readSource(request.Context(), body["source"], "", nil, "", false)
			if err != nil {
				return nil, 0, err
			}
			source = &value
			secrets = readSecrets
		}
		checked := []checkedReference{}
		references := []core.NewReference{}
		for index, raw := range rawReferences {
			part, err := api.readSourcePart(raw, "references["+strconv.Itoa(index)+"]")
			if err != nil {
				return nil, 0, err
			}
			reference, err := api.checkReference(request.Context(), "", source, secrets, part)
			if err != nil {
				return nil, 0, err
			}
			checked = append(checked, reference)
			references = append(references, core.NewReference{Source: core.Source{Kind: reference.part.Kind, Settings: reference.part.Settings}, Where: reference.where})
		}
		var rawSource any
		if source != nil {
			rawSource, err = sourceObject(*source)
			if err != nil {
				return nil, 0, err
			}
		}
		project, err := api.options.Repository.CreateProject(request.Context(), name, core.ProjectStart{Source: rawSource, Message: message, References: references})
		if err != nil {
			return nil, 0, err
		}
		if store := api.options.Settings; store != nil {
			if source != nil {
				if err := store.SaveSourceSecrets(project.ID, secrets); err != nil {
					return nil, 0, err
				}
			}
			data, _ := json.Marshal(project.References)
			var refs []core.Reference
			if err := json.Unmarshal(data, &refs); err != nil {
				return nil, 0, err
			}
			for index, reference := range checked {
				if len(reference.secrets) > 0 && index < len(refs) {
					if err := store.SaveReferenceSecrets(project.ID, refs[index].ID, reference.secrets); err != nil {
						return nil, 0, err
					}
				}
			}
		}
		return object{"project": project}, 201, nil
	})
	api.add("POST", "/api/projects/:projectId/references", func(request *http.Request) (any, int, error) {
		body, err := readJSON(request)
		if err != nil {
			return nil, 0, err
		}
		part, err := api.readSourcePart(body["reference"], "source")
		if err != nil {
			return nil, 0, err
		}
		id := request.PathValue("projectId")
		project, err := api.options.Repository.GetProject(request.Context(), id)
		if err != nil {
			return nil, 0, err
		}
		source, err := sources.DecodeDefinition(project.Source)
		if err != nil {
			return nil, 0, err
		}
		secrets := object{}
		if store := api.options.Settings; store != nil {
			secrets = sources.ReferenceSecretInputs(store.SourceSecrets(id), project.WritableSources().PrimaryID)
		}
		reference, err := api.checkReference(request.Context(), id, source, secrets, part)
		if err != nil {
			return nil, 0, err
		}
		added, err := api.options.Repository.AddProjectReference(request.Context(), id, core.Source{Kind: reference.part.Kind, Settings: reference.part.Settings}, reference.where)
		if err != nil {
			return nil, 0, err
		}
		if store := api.options.Settings; store != nil && len(reference.secrets) > 0 {
			if err := store.SaveReferenceSecrets(id, added.ID, reference.secrets); err != nil {
				return nil, 0, err
			}
		}
		project, err = api.options.Repository.GetProject(request.Context(), id)
		return object{"project": project}, 200, err
	})
	api.add("DELETE", "/api/projects/:projectId/references/:referenceId", func(request *http.Request) (any, int, error) {
		id, ref := request.PathValue("projectId"), request.PathValue("referenceId")
		project, err := api.options.Repository.RemoveProjectReference(request.Context(), id, ref)
		if err != nil {
			return nil, 0, err
		}
		if api.options.Workspaces != nil {
			if err := api.options.Workspaces.InvalidateReference(request.Context(), id, ref); err != nil {
				return nil, 0, err
			}
		}
		if store := api.options.Settings; store != nil {
			if err := store.SaveReferenceSecrets(id, ref, object{}); err != nil {
				return nil, 0, err
			}
		}
		return object{"project": project}, 200, nil
	})
}
