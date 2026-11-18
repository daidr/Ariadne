package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/modelcatalog"
	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

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
	looked, _ := api.catalog.Describe(ctx, names, hint)
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
func (api *API) registerModelCatalog() {
	api.add("POST", "/api/settings/model-list", func(r *http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, domainerr.NotFound("settings")
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		draft, err := api.options.Settings.DraftProvider(body["provider"], api.modelKinds())
		if err != nil {
			return nil, 0, err
		}
		provider, exists := api.options.Models.Get(draft.Info.Kind)
		if !exists {
			return nil, 0, badInput("provider.kind")
		}
		if provider.Models == nil {
			return object{"models": nil}, 200, nil
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		headers := map[string]string{}
		for key, value := range draft.Headers {
			if value, ok := value.(string); ok {
				headers[key] = value
			}
		}
		listed, err := provider.Models(ctx, draft.Settings, sdk.ModelListOptions{Headers: headers})
		if err != nil {
			return object{"error": domainerr.New("error.modelCheck", object{"detail": err.Error()})}, 502, nil
		}
		listed = modelcatalog.Entries(listed)
		known, info := api.describeListed(r.Context(), listed, draft.Hint)
		ids := []string{}
		byID := map[string]sdk.ListedModel{}
		for _, model := range listed {
			ids = append(ids, model.ID)
			byID[model.ID] = model
		}
		if !provider.Info.Program {
			ids = modelcatalog.InOrder(ids, known)
		}
		entries := []any{}
		for _, id := range ids {
			entry := object{"id": id}
			if title := byID[id].Title; title != "" {
				entry["title"] = title
			}
			if facts, exists := info[id]; exists {
				entry["info"] = facts
			}
			entries = append(entries, entry)
		}
		return object{"models": entries}, 200, nil
	})
	api.add("GET", "/api/settings/model-info", func(r *http.Request) (any, int, error) {
		model := prefix(textutil.Trim(r.URL.Query().Get("model")), 200)
		if model == "" {
			return nil, 0, badInput("model")
		}
		hint := prefix(r.URL.Query().Get("hint"), 500)
		info, err := api.catalog.Describe(r.Context(), []string{model}, hint)
		if err != nil {
			return object{"error": domainerr.New("error.priceCatalog", object{"detail": err.Error()})}, 502, nil
		}
		facts, exists := info[model]
		if !exists {
			return object{"error": domainerr.New("error.priceUnknown", object{"model": model})}, 404, nil
		}
		return object{"info": facts}, 200, nil
	})
	api.add("GET", "/api/settings/found", func(*http.Request) (any, int, error) {
		found := []any{}
		for _, setup := range api.foundSetups() {
			found = append(found, object{"id": setup.ID, "kind": setup.Kind, "title": setup.Title, "from": setup.From})
		}
		return object{"found": found}, 200, nil
	})
	api.add("POST", "/api/settings/found/use", func(r *http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, domainerr.NotFound("settings")
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		id, err := required(body, "id")
		if err != nil {
			return nil, 0, err
		}
		var selected *settings.FoundSetup
		for _, setup := range api.foundSetups() {
			if setup.ID == id {
				selected = &setup
				break
			}
		}
		if selected == nil {
			return nil, 0, domainerr.NotFound(id)
		}
		draft, err := settings.FoundProvider(*selected, api.modelKinds())
		if err != nil {
			return nil, 0, err
		}
		provider, exists := api.options.Models.Get(selected.Kind)
		if !exists {
			return nil, 0, badInput("id")
		}
		listed := []sdk.ListedModel{}
		if provider.Models != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			listed, err = provider.Models(ctx, draft.Settings, sdk.ModelListOptions{})
			if err != nil {
				return nil, 0, domainerr.New("error.modelCheck", object{"detail": err.Error()})
			}
		}
		listed = modelcatalog.Entries(listed)
		known, info := api.describeListed(r.Context(), listed, draft.Hint)
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
			return nil, 0, domainerr.New("error.modelNone", nil)
		}
		var facts object
		if value, exists := info[chosen]; exists {
			data, _ := json.Marshal(value)
			_ = json.Unmarshal(data, &facts)
		}
		now := time.Now()
		if api.options.Now != nil {
			now = api.options.Now()
		}
		saved, err := api.options.Settings.UseFound(*selected, chosen, facts, now)
		if err != nil {
			return nil, 0, err
		}
		resolved, err := api.options.Settings.ResolveSaved(saved, api.modelKinds())
		if err != nil {
			return nil, 0, err
		}
		problems := []any{}
		if resolved != nil {
			problem, err := api.options.Models.CheckModel(r.Context(), resolved, api.options.Settings.Locale())
			if err != nil {
				return nil, 0, err
			}
			if problem != nil {
				problems = append(problems, object{"model": saved["id"], "provider": saved["provider"], "problem": problem})
			}
		}
		view := api.options.Settings.PublicModels(api.modelKinds())
		view["problems"] = problems
		return view, 200, nil
	})
}
