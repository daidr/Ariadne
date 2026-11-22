package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/modelcatalog"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

type listedModelResult struct {
	ID    string
	Title string
	Info  *modelcatalog.Info
}

func (api *API) describeListed(ctx context.Context, listed []sdk.ListedModel, hint string) (map[string]modelcatalog.Info, map[string]modelcatalog.Info) {
	names := []string{}
	seen := map[string]bool{}
	for _, model := range listed {
		name := model.Resolves
		if name == "" {
			name = model.ID
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	describeCtx, cancel := context.WithTimeout(ctx, modelCatalogTimeout)
	defer cancel()
	looked, _ := api.catalog.Describe(describeCtx, names, hint)
	known, info := map[string]modelcatalog.Info{}, map[string]modelcatalog.Info{}
	for _, model := range listed {
		name := model.Resolves
		if name == "" {
			name = model.ID
		}
		facts, exists := looked[name]
		if exists {
			known[model.ID] = facts
		}
		if exists || model.Efforts != nil {
			if model.Efforts != nil {
				facts.Efforts = model.Efforts
			}
			info[model.ID] = facts
		}
	}
	return known, info
}
func (api *API) findContext() sdk.FindContext {
	if api.options.FindContext != nil {
		return *api.options.FindContext
	}
	home, _ := os.UserHomeDir()
	return sdk.FindContext{Env: process.Environ(), Home: home, Which: func(program string) bool { _, err := exec.LookPath(program); return err == nil }}
}
func (api *API) foundSetups() []settings.FoundSetup {
	if api.options.Settings == nil || api.options.Models == nil {
		return []settings.FoundSetup{}
	}
	return api.options.Settings.FoundSetups(api.findContext(), api.options.Models.Providers())
}

func (api *API) listProviderModels(ctx context.Context, rawProvider any) ([]listedModelResult, bool, error) {
	if api.options.Settings == nil {
		return nil, false, domainerr.NotFound("settings")
	}
	draft, err := api.options.Settings.DraftProvider(rawProvider, api.modelKinds(), api.modelValidate())
	if err != nil {
		return nil, false, err
	}
	if api.options.Models == nil {
		return nil, false, badInput("provider.kind")
	}
	provider, exists := api.options.Models.Get(draft.Info.Kind)
	if !exists {
		return nil, false, badInput("provider.kind")
	}
	if provider.Models == nil {
		return nil, false, nil
	}
	listCtx, cancel := context.WithTimeout(ctx, modelCatalogTimeout)
	defer cancel()
	headers := map[string]string{}
	for key, value := range draft.Headers {
		if value, ok := value.(string); ok {
			headers[key] = value
		}
	}
	listed, err := provider.Models(listCtx, draft.Settings, sdk.ModelListOptions{Headers: headers})
	if err != nil {
		return nil, false, domainerr.New("error.modelCheck", object{"detail": err.Error()})
	}
	listed = modelcatalog.Entries(listed)
	known, info := api.describeListed(ctx, listed, draft.Hint)
	ids := []string{}
	byID := map[string]sdk.ListedModel{}
	for _, model := range listed {
		ids = append(ids, model.ID)
		byID[model.ID] = model
	}
	if !provider.Info.Program {
		ids = modelcatalog.InOrder(ids, known)
	}
	entries := make([]listedModelResult, 0, len(ids))
	for _, id := range ids {
		item := listedModelResult{ID: id, Title: byID[id].Title}
		if facts, exists := info[id]; exists {
			copyFacts := facts
			item.Info = &copyFacts
		}
		entries = append(entries, item)
	}
	return entries, true, nil
}

func (api *API) lookupModelInfo(ctx context.Context, rawModel, rawHint string) (modelcatalog.Info, error) {
	model := prefix(textutil.Trim(rawModel), 200)
	if model == "" {
		return modelcatalog.Info{}, badInput("model")
	}
	hint := prefix(rawHint, 500)
	describeCtx, cancel := context.WithTimeout(ctx, modelCatalogTimeout)
	defer cancel()
	info, err := api.catalog.Describe(describeCtx, []string{model}, hint)
	if err != nil {
		return modelcatalog.Info{}, domainerr.New("error.priceCatalog", object{"detail": err.Error()})
	}
	facts, exists := info[model]
	if !exists {
		return modelcatalog.Info{}, domainerr.New("error.priceUnknown", object{"model": model})
	}
	return facts, nil
}

func (api *API) useFoundSetup(ctx context.Context, id string) (object, []modelProblem, error) {
	if api.options.Settings == nil {
		return nil, nil, domainerr.NotFound("settings")
	}
	if textutil.Trim(id) == "" {
		return nil, nil, badInput("id")
	}
	var selected *settings.FoundSetup
	for _, setup := range api.foundSetups() {
		if setup.ID == id {
			copySetup := setup
			selected = &copySetup
			break
		}
	}
	if selected == nil {
		return nil, nil, domainerr.NotFound(id)
	}
	draft, err := settings.FoundProvider(*selected, api.modelKinds())
	if err != nil {
		return nil, nil, err
	}
	if api.options.Models == nil {
		return nil, nil, badInput("id")
	}
	provider, exists := api.options.Models.Get(selected.Kind)
	if !exists {
		return nil, nil, badInput("id")
	}
	listed := []sdk.ListedModel{}
	if provider.Models != nil {
		listCtx, cancel := context.WithTimeout(ctx, modelCatalogTimeout)
		defer cancel()
		listed, err = provider.Models(listCtx, draft.Settings, sdk.ModelListOptions{})
		if err != nil {
			return nil, nil, domainerr.New("error.modelCheck", object{"detail": err.Error()})
		}
	}
	listed = modelcatalog.Entries(listed)
	known, info := api.describeListed(ctx, listed, draft.Hint)
	ids := []string{}
	hasDefault := false
	for _, model := range listed {
		ids = append(ids, model.ID)
		hasDefault = hasDefault || model.ID == "default"
	}
	chosen := modelcatalog.SuggestModel(ids, known)
	if provider.Info.Program && hasDefault {
		chosen = "default"
	}
	if chosen == "" {
		return nil, nil, domainerr.New("error.modelNone", nil)
	}
	var facts object
	if value, exists := info[chosen]; exists {
		data, _ := json.Marshal(value)
		_ = json.Unmarshal(data, &facts)
	}
	saved, err := api.options.Settings.UseFound(*selected, chosen, facts, api.now())
	if err != nil {
		return nil, nil, err
	}
	resolved, err := api.options.Settings.ResolveSaved(saved, api.modelKinds())
	if err != nil {
		return nil, nil, err
	}
	problems := []modelProblem{}
	if resolved != nil {
		probeCtx, cancel := context.WithTimeout(ctx, modelProbeTimeout)
		defer cancel()
		problem, err := api.options.Models.CheckModel(probeCtx, resolved, api.options.Settings.Locale())
		if err != nil {
			return nil, nil, err
		}
		if problem != nil {
			modelID, _ := saved["id"].(string)
			providerID, _ := saved["provider"].(string)
			problems = append(problems, modelProblem{Model: modelID, Provider: providerID, Problem: problem})
		}
	}
	return api.options.Settings.PublicModels(api.modelKinds()), problems, nil
}
