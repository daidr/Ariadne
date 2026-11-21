package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/settings"
)

func TestSettingsConnectRPC(t *testing.T) {
	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer draft-secret" {
			t.Error("missing search auth")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[]}`)
	}))
	defer searchServer.Close()

	directory := t.TempDir()
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(directory, "data.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(directory, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store})
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

	// Search settings & check
	if _, err := client.GetSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.GetSearchSettingsRequest{})); err != nil {
		t.Fatalf("GetSearchSettings: %v", err)
	}
	provider := "deepseek"
	baseURL := searchServer.URL
	checkRes, err := client.CheckSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.CheckSearchSettingsRequest{
		Provider: &provider,
		Services: map[string]*ariadnev1.SearchServiceInput{
			"deepseek": {
				BaseUrl:      &baseURL,
				ApiKeyAction: &ariadnev1.SearchServiceInput_ApiKey{ApiKey: "draft-secret"},
			},
		},
	}))
	if err != nil || !checkRes.Msg.GetOk() {
		t.Fatalf("CheckSearchSettings: res=%+v err=%v", checkRes, err)
	}
	savedBaseURL := "https://example.test/v1/tools/"
	savedSearch, err := client.SaveSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveSearchSettingsRequest{
		Provider: &provider,
		Services: map[string]*ariadnev1.SearchServiceInput{
			"deepseek": {
				BaseUrl:      &savedBaseURL,
				ApiKeyAction: &ariadnev1.SearchServiceInput_ApiKey{ApiKey: "saved-private"},
			},
		},
	}))
	if err != nil || !savedSearch.Msg.GetSearch().GetKeySet() {
		t.Fatalf("SaveSearchSettings: res=%+v err=%v", savedSearch, err)
	}
	// Empty apiKey keeps stored secret
	keptSearch, err := client.SaveSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveSearchSettingsRequest{
		Services: map[string]*ariadnev1.SearchServiceInput{
			"deepseek": {
				ApiKeyAction: &ariadnev1.SearchServiceInput_ApiKey{ApiKey: ""},
			},
		},
	}))
	if err != nil || !keptSearch.Msg.GetSearch().GetKeySet() {
		t.Fatalf("SaveSearchSettings keep secret: res=%+v err=%v", keptSearch, err)
	}
	// Clear apiKey removes stored secret
	clearedSearch, err := client.SaveSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveSearchSettingsRequest{
		Services: map[string]*ariadnev1.SearchServiceInput{
			"deepseek": {
				ApiKeyAction: &ariadnev1.SearchServiceInput_ClearApiKey{ClearApiKey: true},
			},
		},
	}))
	if err != nil || clearedSearch.Msg.GetSearch().GetKeySet() {
		t.Fatalf("SaveSearchSettings clear secret: res=%+v err=%v", clearedSearch, err)
	}
	if _, err := client.CheckSearchSettings(t.Context(), connect.NewRequest(&ariadnev1.CheckSearchSettingsRequest{})); err == nil {
		t.Fatal("expected empty search check to fail without key")
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
