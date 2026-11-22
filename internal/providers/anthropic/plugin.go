package anthropic

import (
	"context"
	"iter"
	"net/http"
	"time"

	"github.com/daidr/Ariadne/internal/modelhttp"
	"github.com/daidr/Ariadne/sdk"
)

const PluginID = "model-anthropic"
const DefaultBaseURL = "https://api.anthropic.com"

func stringSetting(settings map[string]any, key string) string {
	value, _ := settings[key].(string)
	return value
}

func settingsFrom(values map[string]any) Settings {
	return Settings{
		BaseURL: stringSetting(values, "baseUrl"),
		APIKey:  stringSetting(values, "apiKey"),
	}
}

func ModelProvider(client *http.Client, now func() time.Time) sdk.ModelProvider {
	api := Provider{Client: client, Now: now}
	return sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{
			Kind:  "anthropic",
			Title: sdk.LocalizedText{"zh-CN": "Anthropic 接口", "en": "Anthropic API"},
			Capabilities: sdk.ModelProviderCapabilities{
				ReasoningScope: sdk.ReasoningScopeProvider,
			},
			Settings: sdk.SettingsSchema{
				Type: "object",
				Properties: map[string]sdk.SettingField{
					"baseUrl": {
						Type:          "string",
						Title:         sdk.LocalizedText{"zh-CN": "接口地址", "en": "Base URL"},
						NetworkOrigin: true,
						Default:       DefaultBaseURL,
					},
					"apiKey": {
						Type:   "string",
						Title:  sdk.LocalizedText{"zh-CN": "API Key", "en": "API key"},
						Secret: true,
					},
				},
				Required:   []string{"baseUrl", "apiKey"},
				FieldOrder: []string{"baseUrl", "apiKey"},
			},
		},
		ValidateSettings: modelhttp.ValidateHTTPSettings,
		Stream: func(ctx context.Context, settings map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
			return api.Stream(ctx, settingsFrom(settings), request)
		},
		Models: func(ctx context.Context, settings map[string]any, options sdk.ModelListOptions) ([]sdk.ListedModel, error) {
			return api.Models(ctx, settingsFrom(settings), options.Headers)
		},
		Describe: func(settings map[string]any, request sdk.Request) any {
			return RequestBody(settingsFrom(settings), request)
		},
		SearchAvailable: func(settings map[string]any) bool {
			return SearchAvailable(settingsFrom(settings))
		},
		Search: func(ctx context.Context, settings map[string]any, request sdk.SearchRequest) (sdk.SearchAnswer, error) {
			return api.Search(ctx, settingsFrom(settings), request)
		},
		Find: func(ctx sdk.FindContext) []sdk.FoundProvider {
			found := Find(ctx.Env)
			if found == nil {
				return nil
			}
			return []sdk.FoundProvider{{
				ID:       "ANTHROPIC_API_KEY",
				From:     map[string]string{"env": "ANTHROPIC_API_KEY"},
				Settings: found["settings"].(map[string]any),
			}}
		},
	}
}

func Plugin(client *http.Client, now func() time.Time) sdk.Plugin {
	return sdk.Plugin{
		ID:       PluginID,
		Name:     sdk.LocalizedText{"zh-CN": "Anthropic 接口", "en": "Anthropic API"},
		Provides: sdk.PluginProvides{Models: []string{"anthropic"}},
		Setup: func(_ context.Context, pc sdk.PluginContext) (func(context.Context) error, error) {
			if _, err := pc.RegisterModel(ModelProvider(client, now)); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}
}
