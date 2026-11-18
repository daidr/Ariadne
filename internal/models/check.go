package models

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func (registry *Registry) CheckModel(ctx context.Context, model *settings.ResolvedModel, locale string) (problem *domainerr.Error, err error) {
	defer func() {
		if value := recover(); value != nil {
			problem = domainerr.New("error.modelCheck", map[string]any{"detail": fmt.Sprint(value)})
			err = nil
		}
	}()
	if model == nil {
		return nil, nil
	}
	provider, exists := registry.Get(model.Provider.Kind)
	if !exists {
		return nil, domainerr.New("error.modelUnknown", map[string]any{"provider": model.Provider.Kind})
	}
	if provider.Check != nil {
		problem, err := provider.Check(ctx, model.Settings)
		if err != nil {
			return nil, err
		}
		if problem == nil {
			return nil, nil
		}
		detail, exists := problem[locale]
		if !exists {
			detail = problem["zh-CN"]
		}
		return domainerr.New("error.modelCheck", map[string]any{"detail": detail}), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cache := false
	request := sdk.Request{Model: model.Model, Messages: []sdk.Message{{Role: "user", Content: "Hi"}}, Tools: []sdk.Tool{}, Cache: &cache}
	if headers, ok := model.Request["headers"].(map[string]any); ok {
		request.Headers = map[string]string{}
		for key, raw := range headers {
			if value, ok := raw.(string); ok {
				request.Headers[key] = value
			}
		}
	}
	request.ExtraBody, _ = model.Request["extraBody"].(map[string]any)
	for part := range provider.Stream(ctx, model.Settings, request) {
		if part.Type == "finish" && part.Reason == "error" {
			return domainerr.New("error.modelCheck", map[string]any{"detail": part.Error}), nil
		}
		if part.Type == "alive" {
			continue
		}
		return nil, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
		return domainerr.New("error.modelCheckTimeout", map[string]any{"seconds": 20}), nil
	}
	return nil, nil
}
