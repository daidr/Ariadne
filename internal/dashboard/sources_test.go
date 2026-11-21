package dashboard

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

func testInfo() sdk.DashboardSourceInfo {
	return sdk.DashboardSourceInfo{Kind: "fixture", Title: sdk.LocalizedText{"zh-CN": "测试", "en": "Fixture"}, Description: "named rows", Settings: sdk.SettingsSchema{Type: "object", Required: []string{"query"}, FieldOrder: []string{"query", "count", "mode", "enabled", "token"}, Properties: map[string]sdk.SettingField{
		"query": {Type: "string"}, "count": {Type: "number", Default: float64(10)}, "mode": {Type: "enum", Options: []string{"open", "all"}, Default: "open"}, "enabled": {Type: "boolean"}, "token": {Type: "string", Secret: true},
	}}}
}
func TestDashboardSettingsAndFetch(t *testing.T) {
	info := testInfo()
	values := []any{nil, []any{}, map[string]any{"query": "issues"}, map[string]any{"unknown": "value"}, map[string]any{"token": nil}, map[string]any{"query": 5}, map[string]any{"query": "issues", "mode": "wrong"}, map[string]any{"query": "issues", "count": nil, "enabled": false}}
	checked := []any{}
	for _, value := range values {
		settings, err := CheckSettings(info, value)
		if err != nil {
			checked = append(checked, map[string]any{"ok": false, "problem": err.Error()})
		} else {
			checked = append(checked, map[string]any{"ok": true, "settings": settings})
		}
	}
	fetched := map[string]any{"rows": []any{map[string]any{"value": float64(5)}}}
	registry := New()
	defer registry.Close(t.Context())
	_, err := registry.Register(sdk.DashboardSource{Info: info, Fetch: func(context.Context, map[string]any, string) (map[string]any, error) { return fetched, nil }})
	if err != nil {
		t.Fatal(err)
	}
	actualData, err := registry.Fetch(t.Context(), "fixture", nil, "prj_a", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(checked) != len(values) || !reflect.DeepEqual(actualData, fetched) {
		t.Fatalf("unexpected dashboard fetch: checked=%d data=%+v", len(checked), actualData)
	}
}
func TestDashboardFetchCancellationRemovalAndLimits(t *testing.T) {
	registry := New()
	defer registry.Close(t.Context())
	started := make(chan struct{})
	stopped := make(chan struct{})
	remove, err := registry.Register(sdk.DashboardSource{Info: testInfo(), Fetch: func(ctx context.Context, _ map[string]any, _ string) (map[string]any, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := registry.Fetch(t.Context(), "fixture", nil, "prj_a", time.Second); finished <- err }()
	<-started
	remove()
	if err := <-finished; err == nil {
		t.Fatal("unload did not cancel fetch")
	}
	<-stopped
	for _, value := range []map[string]any{nil, {"rows": strings.Repeat("x", MaxFetchedUnits+1)}} {
		remove, err = registry.Register(sdk.DashboardSource{Info: testInfo(), Fetch: func(context.Context, map[string]any, string) (map[string]any, error) { return value, nil }})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := registry.Fetch(t.Context(), "fixture", nil, "prj_a", time.Second); err == nil {
			t.Fatal("invalid fetched data accepted")
		}
		remove()
	}
	remove, err = registry.Register(sdk.DashboardSource{Info: testInfo(), Fetch: func(ctx context.Context, _ map[string]any, _ string) (map[string]any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Fetch(t.Context(), "fixture", nil, "prj_a", 10*time.Millisecond); err == nil || err.Error() != "the source did not answer in time" {
		t.Fatal("fetch deadline missing", err)
	}
	remove()
	_, err = registry.Register(sdk.DashboardSource{Info: testInfo(), Fetch: func(context.Context, map[string]any, string) (map[string]any, error) { panic("source failed") }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Fetch(t.Context(), "fixture", nil, "prj_a", time.Second); err == nil {
		t.Fatal("panic escaped")
	}
	if err := registry.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(registry.Describe()) != 0 {
		t.Fatal("closed registry retains metadata")
	}
}
func TestDashboardSettingsDoNotPersistSecretDefaultsAndMetadataIsOwned(t *testing.T) {
	info := testInfo()
	field := info.Settings.Properties["token"]
	field.Default = "private"
	info.Settings.Properties["token"] = field
	if _, err := CheckSettings(info, map[string]any{"query": "issues"}); err == nil {
		t.Fatal("secret default could enter event log")
	}
	info = testInfo()
	registry := New()
	defer registry.Close(t.Context())
	_, err := registry.Register(sdk.DashboardSource{Info: info, Fetch: func(context.Context, map[string]any, string) (map[string]any, error) {
		return nil, errors.New("failed")
	}})
	if err != nil {
		t.Fatal(err)
	}
	info.Title["en"] = "mutated"
	delete(info.Settings.Properties, "query")
	copy := registry.Info("fixture")
	copy.Title["en"] = "again"
	delete(copy.Settings.Properties, "query")
	copy = registry.Info("fixture")
	if copy.Title["en"] != "Fixture" || copy.Settings.Properties["query"].Type != "string" {
		t.Fatal("caller mutated registered source")
	}
}
