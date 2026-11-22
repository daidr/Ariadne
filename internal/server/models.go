package server

import (
	"context"
	"sync"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/sdk"
)

var (
	modelProbeTimeout   = 20 * time.Second
	modelCatalogTimeout = 15 * time.Second
	mcpProbeTimeout     = 65 * time.Second
	searchCheckTimeout  = 15 * time.Second
	localBrowseTimeout  = 10 * time.Second
)

type modelProblem struct {
	Model    string
	Provider string
	Problem  *domainerr.Error
}

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

func (api *API) saveModelSettings(ctx context.Context, body object) (object, []modelProblem, error) {
	store := api.options.Settings
	if store == nil {
		return nil, nil, domainerr.NotFound("settings")
	}
	kinds := api.modelKinds()
	changed, err := store.UpdateModels(body, kinds, api.modelValidate())
	if err != nil {
		return nil, nil, err
	}
	timeout := modelProbeTimeout
	timeoutSeconds := max(1, int(timeout/time.Second))
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	problems := make([]*modelProblem, len(changed))
	errs := make([]error, len(changed))
	jobs := make(chan int)
	var group sync.WaitGroup
	locale := store.Locale()
	check := func(index int) {
		model := changed[index].(map[string]any)
		modelID, _ := model["id"].(string)
		providerID, _ := model["provider"].(string)
		if probeCtx.Err() != nil {
			problems[index] = &modelProblem{
				Model:    modelID,
				Provider: providerID,
				Problem:  domainerr.New("error.modelCheckTimeout", object{"seconds": timeoutSeconds}),
			}
			return
		}
		resolved, err := store.ResolveSaved(model, kinds)
		if err != nil {
			errs[index] = err
			return
		}
		if resolved == nil || api.options.Models == nil {
			return
		}
		problem, err := api.options.Models.CheckModel(probeCtx, resolved, locale)
		if err != nil {
			errs[index] = err
			return
		}
		if problem != nil {
			problems[index] = &modelProblem{
				Model:    modelID,
				Provider: providerID,
				Problem:  problem,
			}
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

	found := make([]modelProblem, 0, len(problems))
	for index, problem := range problems {
		if errs[index] != nil {
			return nil, nil, errs[index]
		}
		if problem != nil {
			found = append(found, *problem)
		}
	}
	return store.PublicModels(api.modelKinds()), found, nil
}
