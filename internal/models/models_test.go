package models

import (
	"context"
	"errors"
	"iter"
	"slices"
	"testing"

	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestModelRegistryCheckAndLifecycle(t *testing.T) {
	registry := New()
	closed := false
	provider := sdk.ModelProvider{Info: sdk.ModelProviderInfo{Kind: "fixture", Title: sdk.LocalizedText{"zh-CN": "测试", "en": "Test"}, Settings: sdk.SettingsSchema{Type: "object", Properties: map[string]sdk.SettingField{}}}, Stream: func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) {
			defer func() { closed = true }()
			if !yield(sdk.Part{Type: "text", Text: "hi"}) {
				return
			}
			t.Error("check continued after first answer")
		}
	}}
	remove, err := registry.Register(provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Register(provider); err == nil {
		t.Fatal("duplicate provider registered")
	}
	model := &settings.ResolvedModel{Provider: provider.Info, Model: "model"}
	problem, err := registry.CheckModel(t.Context(), model, "en")
	if err != nil || problem != nil || !closed {
		t.Fatal(problem, err, closed)
	}
	remove()
	remove()
	if len(registry.Providers()) != 0 {
		t.Fatal("removed provider retained")
	}
	provider.Stream = func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] {
		return func(yield func(sdk.Part) bool) { yield(sdk.Part{Type: "finish", Reason: "error", Error: "refused"}) }
	}
	remove, err = registry.Register(provider)
	if err != nil {
		t.Fatal(err)
	}
	problem, err = registry.CheckModel(t.Context(), model, "en")
	if err != nil || problem == nil || problem.Params["detail"] != "refused" {
		t.Fatal(problem, err)
	}
	remove()
	provider.Check = func(context.Context, map[string]any) (sdk.LocalizedText, error) {
		return sdk.LocalizedText{"zh-CN": "中文错误"}, nil
	}
	remove, err = registry.Register(provider)
	if err != nil {
		t.Fatal(err)
	}
	problem, err = registry.CheckModel(t.Context(), model, "en")
	if err != nil || problem == nil || problem.Params["detail"] != "中文错误" {
		t.Fatal(problem, err)
	}
	remove()
}

func TestRegistryForgetAndValidateSettings(t *testing.T) {
	registry := New()
	var forgotten []string
	wantErr := errors.New("cleanup failed")

	noopStream := func(context.Context, map[string]any, sdk.Request) iter.Seq[sdk.Part] {
		return func(func(sdk.Part) bool) {}
	}
	removeA, err := registry.Register(sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{
			Kind: "alpha",
			Capabilities: sdk.ModelProviderCapabilities{
				StatefulSessions:   true,
				PreserveModelOrder: true,
				PreferDefaultModel: true,
			},
		},
		Stream: noopStream,
		ValidateSettings: func(values map[string]any) error {
			if values["command"] == "" {
				return errors.New("empty command")
			}
			return nil
		},
		Forget: func(_ context.Context, conversation string) error {
			forgotten = append(forgotten, "alpha:"+conversation)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer removeA()

	removeB, err := registry.Register(sdk.ModelProvider{
		Info:   sdk.ModelProviderInfo{Kind: "beta"},
		Stream: noopStream,
		Forget: func(_ context.Context, conversation string) error {
			forgotten = append(forgotten, "beta:"+conversation)
			return wantErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer removeB()

	alpha, ok := registry.Get("alpha")
	if !ok || !alpha.Info.HasStatefulSessions() || !alpha.Info.PreservesModelOrder() || !alpha.Info.PrefersDefaultModel() {
		t.Fatalf("expected capability helpers to reflect Capabilities: %+v", alpha.Info)
	}
	if err := registry.ValidateSettings("alpha", map[string]any{"command": ""}); err == nil {
		t.Fatal("expected ValidateSettings to fail for empty command")
	}
	if err := registry.ValidateSettings("alpha", map[string]any{"command": "echo"}); err != nil {
		t.Fatalf("unexpected ValidateSettings error: %v", err)
	}
	if err := registry.ValidateSettings("unknown", map[string]any{}); err != nil {
		t.Fatalf("unknown kind should not fail ValidateSettings: %v", err)
	}

	if err := registry.Forget(t.Context(), ""); err != nil {
		t.Fatalf("empty conversation forget should be a no-op: %v", err)
	}
	if len(forgotten) != 0 {
		t.Fatalf("unexpected forget calls: %v", forgotten)
	}
	err = registry.Forget(t.Context(), "thr_123")
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected joined forget error, got %v", err)
	}
	if !slices.Equal(forgotten, []string{"alpha:thr_123", "beta:thr_123"}) {
		t.Fatalf("expected both providers to receive Forget, got %v", forgotten)
	}
}
