package openai

import (
	"context"
	"iter"
	"net/http"
	"time"

	"github.com/daidr/Ariadne/internal/modelhttp"
	"github.com/daidr/Ariadne/internal/textutil"
	"github.com/daidr/Ariadne/sdk"
)

const PluginID = "model-openai"
const DefaultBaseURL = "https://api.openai.com/v1"

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

func findProviders(ctx sdk.FindContext) []sdk.FoundProvider {
	result := []sdk.FoundProvider{}
	for _, service := range []struct{ key, name, address, base string }{
		{"OPENAI_API_KEY", "OpenAI", "OPENAI_BASE_URL", DefaultBaseURL},
		{"DEEPSEEK_API_KEY", "DeepSeek", "", "https://api.deepseek.com/v1"},
		{"OPENROUTER_API_KEY", "OpenRouter", "", "https://openrouter.ai/api/v1"},
	} {
		key := textutil.Trim(ctx.Env[service.key])
		if key == "" {
			continue
		}
		base := textutil.Trim(ctx.Env[service.address])
		if base == "" {
			base = service.base
		}
		result = append(result, sdk.FoundProvider{
			ID:       service.key,
			Name:     service.name,
			From:     map[string]string{"env": service.key},
			Settings: map[string]any{"baseUrl": base, "apiKey": key},
		})
	}
	return result
}

func ModelProvider(client *http.Client, now func() time.Time) sdk.ModelProvider {
	api := Provider{Client: client, Now: now}
	return sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{
			Kind:  "openai-compatible",
			Title: sdk.LocalizedText{"zh-CN": "OpenAI 兼容接口", "en": "OpenAI-compatible API"},
			Capabilities: sdk.ModelProviderCapabilities{
				ReasoningScope: sdk.ReasoningScopeModel,
			},
			Settings: sdk.SettingsSchema{
				Type: "object",
				Properties: map[string]sdk.SettingField{
					"baseUrl": {
						Type:          "string",
						Title:         sdk.LocalizedText{"zh-CN": "接口地址", "en": "Base URL"},
						Description:   sdk.LocalizedText{"zh-CN": "以 /v1 结尾的地址", "en": "Usually ends with /v1"},
						NetworkOrigin: true,
						Default:       DefaultBaseURL,
					},
					"apiKey": {
						Type:   "string",
						Title:  sdk.LocalizedText{"zh-CN": "API Key", "en": "API key"},
						Secret: true,
					},
				},
				Required:   []string{"baseUrl"},
				FieldOrder: []string{"baseUrl", "apiKey"},
			},
		},
		ValidateSettings: modelhttp.ValidateHTTPSettings,
		Stream: func(ctx context.Context, settings map[string]any, request sdk.Request) iter.Seq[sdk.Part] {
			return api.Stream(ctx, settingsFrom(settings), request)
		},
		Models: func(ctx context.Context, settings map[string]any, options sdk.ModelListOptions) ([]sdk.ListedModel, error) {
			names, err := api.Models(ctx, settingsFrom(settings), options.Headers)
			listed := []sdk.ListedModel{}
			for _, name := range names {
				listed = append(listed, sdk.ListedModel{ID: name})
			}
			return listed, err
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
		Find: findProviders,
	}
}

func Plugin(client *http.Client, now func() time.Time) sdk.Plugin {
	return sdk.Plugin{
		ID:       PluginID,
		Name:     sdk.LocalizedText{"zh-CN": "OpenAI 兼容接口", "en": "OpenAI-compatible API"},
		Provides: sdk.PluginProvides{Models: []string{"openai-compatible"}},
		Setup: func(_ context.Context, pc sdk.PluginContext) (func(context.Context) error, error) {
			if _, err := pc.RegisterModel(ModelProvider(client, now)); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}
}
