package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/searches"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestSettingsConnectRPC(t *testing.T) {
	t.Parallel()
	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer draft-secret" {
			t.Error("missing search auth")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[]}`)
	}))
	defer searchServer.Close()

	directory := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(directory, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	searchRegistry := searches.New()
	for _, provider := range searches.BuiltinProviders(nil) {
		if _, err := searchRegistry.Register(provider); err != nil {
			t.Fatal(err)
		}
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, Searches: searchRegistry})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	server := httptest.NewServer(api)
	defer server.Close()
	client := ariadnev1connect.NewSettingsServiceClient(
		server.Client(),
		server.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)

	mustStruct := func(values map[string]any) *structpb.Struct {
		t.Helper()
		out, err := structpb.NewStruct(values)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Search settings & check
	if _, err := client.GetSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.GetSearchSettingsRequest{})); err != nil {
		t.Fatalf("GetSearchSettings: %v", err)
	}
	provider := "deepseek"
	checkRes, err := client.CheckSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.CheckSearchSettingsRequest{
		Provider: &provider,
		Services: map[string]*structpb.Struct{
			"deepseek": mustStruct(map[string]any{
				"baseUrl": searchServer.URL,
				"apiKey":  "draft-secret",
			}),
		},
	}))
	if err != nil || !checkRes.Msg.GetOk() {
		t.Fatalf("CheckSearchSettings: res=%+v err=%v", checkRes, err)
	}
	if strings.Contains(checkRes.Msg.String(), "draft-secret") {
		t.Fatalf("CheckSearchSettings leaked secret: %s", checkRes.Msg.String())
	}

	savedSearch, err := client.SaveSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveSearchSettingsRequest{
		Provider: &provider,
		Services: map[string]*structpb.Struct{
			"deepseek": mustStruct(map[string]any{
				"baseUrl": "https://example.test/v1/tools/",
				"apiKey":  "saved-private",
			}),
		},
	}))
	if err != nil || savedSearch.Msg.GetSearch().GetProvider() != "deepseek" || !savedSearch.Msg.GetSearch().GetReady() || !savedSearch.Msg.GetSearch().GetKeySet() {
		t.Fatalf("SaveSearchSettings: res=%+v err=%v", savedSearch, err)
	}
	if strings.Contains(savedSearch.Msg.String(), "saved-private") {
		t.Fatalf("SaveSearchSettings leaked secret: %s", savedSearch.Msg.String())
	}

	// Empty apiKey keeps stored secret
	keptSearch, err := client.SaveSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveSearchSettingsRequest{
		Services: map[string]*structpb.Struct{
			"deepseek": mustStruct(map[string]any{
				"apiKey": "",
			}),
		},
	}))
	if err != nil || !keptSearch.Msg.GetSearch().GetKeySet() || !keptSearch.Msg.GetSearch().GetReady() {
		t.Fatalf("SaveSearchSettings keep secret: res=%+v err=%v", keptSearch, err)
	}

	// Clear apiKey removes stored secret
	clearedSearch, err := client.SaveSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveSearchSettingsRequest{
		Services: map[string]*structpb.Struct{
			"deepseek": mustStruct(map[string]any{
				"apiKey": nil,
			}),
		},
	}))
	if err != nil || clearedSearch.Msg.GetSearch().GetKeySet() || clearedSearch.Msg.GetSearch().GetReady() {
		t.Fatalf("SaveSearchSettings clear secret: res=%+v err=%v", clearedSearch, err)
	}

	if _, err := client.CheckSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.CheckSearchSettingsRequest{})); err == nil {
		t.Fatal("expected empty search check to fail without key")
	}

	// Verify dynamic external search provider appears automatically in GetSearchSettings.
	if _, err := searchRegistry.Register(sdk.SearchProvider{
		Kind:  "custom-ext",
		Title: sdk.LocalizedText{"zh-CN": "扩展搜索", "en": "Extension Search"},
		Search: func(_ context.Context, _ string, _ int) ([]sdk.SearchResult, error) {
			return []sdk.SearchResult{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	extSearch, err := client.GetSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.GetSearchSettingsRequest{}))
	if err != nil {
		t.Fatalf("GetSearchSettings after dynamic register: %v", err)
	}
	foundExt := false
	for _, svc := range extSearch.Msg.GetSearch().GetServices() {
		if svc.GetKind() == "custom-ext" {
			foundExt = true
			break
		}
	}
	if !foundExt {
		t.Fatalf("dynamic search provider missing: %+v", extSearch.Msg.GetSearch().GetServices())
	}

	// Work settings
	if _, err := client.GetWorkSettings(t.Context(), connect.NewRequest(&ariadnev1.GetWorkSettingsRequest{})); err != nil {
		t.Fatalf("GetWorkSettings: %v", err)
	}
	instructions := " plain words "
	cmdTimeout := int32(60)
	workRes, err := client.SaveWorkSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveWorkSettingsRequest{
		Instructions: &instructions,
		Rules: &ariadnev1.CommandRuleList{
			Items: []*ariadnev1.CommandRule{{Pattern: "git push*", Action: "ask"}},
		},
		Limits: &ariadnev1.WorkLimitsInput{CommandTimeout: &cmdTimeout},
	}))
	if err != nil || workRes.Msg.GetWork().GetLimits().GetCommandTimeout() != 60 {
		t.Fatalf("SaveWorkSettings: res=%+v err=%v", workRes, err)
	}
	if _, err := client.SaveWorkSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveWorkSettingsRequest{
		Rules: &ariadnev1.CommandRuleList{
			Items: []*ariadnev1.CommandRule{{Pattern: "", Action: "ask"}},
		},
	})); err == nil {
		t.Fatal("expected invalid work rule to fail")
	} else if connectErr, _ := extractLocalizedError(t, err); connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", connectErr.Code())
	}

	// Input settings
	if _, err := client.GetInputSettings(t.Context(), connect.NewRequest(&ariadnev1.GetInputSettingsRequest{})); err != nil {
		t.Fatalf("GetInputSettings: %v", err)
	}
	inputRes, err := client.SaveInputSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveInputSettingsRequest{
		SubmitOn: "mod-enter",
		Snippets: []*ariadnev1.Snippet{{Id: "a", Name: " 常用 ", Text: "prompt"}},
		Keys:     map[string]string{"focus": "alt+f"},
	}))
	if err != nil || inputRes.Msg.GetInput().GetSubmitOn() != "mod-enter" || len(inputRes.Msg.GetInput().GetSnippets()) != 1 {
		t.Fatalf("SaveInputSettings: res=%+v err=%v", inputRes, err)
	}

	// Concurrency settings
	if _, err := client.GetConcurrency(t.Context(), connect.NewRequest(&ariadnev1.GetConcurrencyRequest{})); err != nil {
		t.Fatalf("GetConcurrency: %v", err)
	}
	concRes, err := client.SaveConcurrency(t.Context(), connect.NewRequest(&ariadnev1.SaveConcurrencyRequest{
		Slots:    5,
		Reserved: 0,
	}))
	if err != nil || concRes.Msg.GetConcurrency().GetSlots() != 5 {
		t.Fatalf("SaveConcurrency: res=%+v err=%v", concRes, err)
	}
	if _, err := client.SaveConcurrency(t.Context(), connect.NewRequest(&ariadnev1.SaveConcurrencyRequest{
		Slots:    0,
		Reserved: 0,
	})); err == nil {
		t.Fatal("expected zero slots to fail")
	} else if connectErr, _ := extractLocalizedError(t, err); connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", connectErr.Code())
	}
}
