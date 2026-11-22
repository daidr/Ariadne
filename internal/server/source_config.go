package server

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

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
