package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/sdk"
)

func (api *API) modelKinds() []sdk.ModelProviderInfo {
	if api.options.Models == nil {
		return []sdk.ModelProviderInfo{}
	}
	return api.options.Models.Kinds()
}
func (api *API) modelValidate() func(string, map[string]any) error {
	if api.options.Models == nil {
		return nil
	}
	return api.options.Models.ValidateSettings
}
func (api *API) registerModels() {
	api.add("GET", "/api/settings/models", func(*http.Request) (any, int, error) {
		if api.options.Settings == nil {
			return nil, 0, domainerr.NotFound("settings")
		}
		return api.options.Settings.PublicModels(api.modelKinds()), 200, nil
	})
	api.add("PUT", "/api/settings/models", func(r *http.Request) (any, int, error) {
		store := api.options.Settings
		if store == nil {
			return nil, 0, domainerr.NotFound("settings")
		}
		body, err := readJSON(r)
		if err != nil {
			return nil, 0, err
		}
		changed, err := store.UpdateModels(body, api.modelKinds(), api.modelValidate())
		if err != nil {
			return nil, 0, err
		}
		problems := make([]any, len(changed))
		errors := make([]error, len(changed))
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		jobs := make(chan int)
		var group sync.WaitGroup
		locale := store.Locale()
		kinds := api.modelKinds()
		check := func(index int) {
			model := changed[index].(map[string]any)
			if ctx.Err() != nil {
				problems[index] = object{"model": model["id"], "provider": model["provider"], "problem": domainerr.New("error.modelCheckTimeout", object{"seconds": 20})}
				return
			}
			resolved, err := store.ResolveSaved(model, kinds)
			if err != nil {
				errors[index] = err
				return
			}
			if resolved == nil || api.options.Models == nil {
				return
			}
			problem, err := api.options.Models.CheckModel(ctx, resolved, locale)
			if err != nil {
				errors[index] = err
				return
			}
			if problem != nil {
				problems[index] = object{"model": model["id"], "provider": model["provider"], "problem": problem}
			}
		}
		for range min(16, len(changed)) {
			group.Add(1)
			go func() {
				defer group.Done()
				for index := range jobs {
					check(index)
				}
			}()
		}
		for index := range changed {
			jobs <- index
		}
		close(jobs)
		group.Wait()
		found := []any{}
		for index, problem := range problems {
			if errors[index] != nil {
				return nil, 0, errors[index]
			}
			if problem != nil {
				found = append(found, problem)
			}
		}
		view := store.PublicModels(api.modelKinds())
		view["problems"] = found
		return view, 200, nil
	})
}
