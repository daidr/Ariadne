package sources

import (
	"context"
	"reflect"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestContextRegistrationOwnsMetadataAndUnloads(t *testing.T) {
	registry := New()
	provider := sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "test", Title: sdk.LocalizedText{"zh-CN": "测试"}, Provides: []string{"workspace"}, Settings: sdk.SettingsSchema{Type: "object", Properties: map[string]sdk.SettingField{"token": {Type: "string", Secret: true, Title: sdk.LocalizedText{"zh-CN": "密钥"}}}}}, Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) { return sdk.Capabilities{}, nil }}
	unload, err := registry.Register(provider)
	if err != nil {
		t.Fatal(err)
	}
	provider.Info.Title["zh-CN"] = "wrong"
	provider.Info.Provides[0] = "wrong"
	loaded, ok := registry.Get("test")
	if !ok || loaded.Info.Title["zh-CN"] != "测试" || loaded.Info.Provides[0] != "workspace" {
		t.Fatal(loaded)
	}
	loaded.Info.Title["zh-CN"] = "wrong"
	delete(loaded.Info.Settings.Properties, "token")
	loaded, _ = registry.Get("test")
	if !reflect.DeepEqual(SecretFields(loaded.Info), []string{"token"}) || loaded.Info.Title["zh-CN"] != "测试" {
		t.Fatal(loaded)
	}
	if _, err := registry.Register(provider); err == nil {
		t.Fatal("duplicate kind accepted")
	}
	unload()
	unload()
	if _, exists := registry.Get("test"); exists || len(registry.Kinds()) != 0 {
		t.Fatal("unloaded provider retained")
	}
	if _, err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
}
