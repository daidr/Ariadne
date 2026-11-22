package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/modelcatalog"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/providers"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestModelListingCatalogAndFoundSetupConnect(t *testing.T) {
	catalogJSON := `{"openai":{"models":{"big":{"cost":{"input":1,"output":2},"limit":{"context":128000,"output":4096},"modalities":{"input":["text","image"]},"tool_call":true,"reasoning":false,"release_date":"2026-11-01"},"old":{"tool_call":true,"release_date":"2025-01-01"},"embedding":{"tool_call":false,"release_date":"2026-11-02"}}}}`
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/catalog" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, catalogJSON)
			return
		}
		if r.Header.Get("Authorization") != "Bearer private" {
			t.Error("wrong provider authorization", r.Header)
		}
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"unknown","created":5},{"id":"old","created":2},{"id":"big","created":1},{"id":"embedding","created":6},{"id":"big","created":0}]}`)
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer service.Close()

	home := t.TempDir()
	env := map[string]string{"OPENAI_API_KEY": "private", "OPENAI_BASE_URL": service.URL + "/v1"}
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
	modelReg := models.New()
	for _, provider := range providers.HTTPProviders(service.Client()) {
		remove, err := modelReg.Register(provider)
		if err != nil {
			t.Fatal(err)
		}
		defer remove()
	}
	catalog := modelcatalog.New(modelcatalog.Options{URL: service.URL + "/catalog", Client: service.Client()})
	defer catalog.Close(context.Background())
	api, err := New(Options{
		Repository:  repo,
		Token:       "test-token",
		Settings:    store,
		Models:      modelReg,
		Catalog:     catalog,
		FindContext: &sdk.FindContext{Env: env, Home: home, Which: func(string) bool { return false }},
		Now:         func() time.Time { return time.UnixMilli(1800000000000) },
	})
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
	checkNoLeak := func(msg proto.Message) {
		t.Helper()
		raw, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "private") {
			t.Fatal("discovery leaked credentials")
		}
	}

	listedRes, err := client.ListProviderModels(t.Context(), connect.NewRequest(&ariadnev1.ListProviderModelsRequest{
		Provider: &ariadnev1.DraftModelProviderInput{
			Kind:     "openai-compatible",
			Settings: mustStruct(t, object{"baseUrl": service.URL + "/v1", "apiKey": "private"}),
		},
	}))
	if err != nil {
		t.Fatalf("ListProviderModels: %v", err)
	}
	if len(listedRes.Msg.GetModels().GetItems()) == 0 {
		t.Fatalf("expected listed models, got %+v", listedRes.Msg)
	}
	checkNoLeak(listedRes.Msg)

	infoRes, err := client.GetModelInfo(t.Context(), connect.NewRequest(&ariadnev1.GetModelInfoRequest{Model: "big"}))
	if err != nil {
		t.Fatalf("GetModelInfo(big): %v", err)
	}
	if infoRes.Msg.GetInfo().GetContextTokens() != 128000 || infoRes.Msg.GetInfo().GetPrice().GetInput() != 1 {
		t.Fatalf("unexpected model info: %+v", infoRes.Msg.GetInfo())
	}

	if _, err := client.GetModelInfo(t.Context(), connect.NewRequest(&ariadnev1.GetModelInfoRequest{Model: "unknown"})); err == nil {
		t.Fatal("expected unknown model info to fail")
	} else if connectErr, localized := extractLocalizedError(t, err); connectErr.Code() != connect.CodeNotFound || localized == nil || localized.Key != "error.priceUnknown" {
		t.Fatalf("unexpected unknown model error: code=%v localized=%v", connectErr.Code(), localized)
	}

	if _, err := client.GetModelInfo(t.Context(), connect.NewRequest(&ariadnev1.GetModelInfoRequest{})); err == nil {
		t.Fatal("expected empty model info to fail")
	} else if connectErr, _ := extractLocalizedError(t, err); connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %v", connectErr.Code())
	}

	foundRes, err := client.ListFoundSetups(t.Context(), connect.NewRequest(&ariadnev1.ListFoundSetupsRequest{}))
	if err != nil || len(foundRes.Msg.GetFound()) == 0 {
		t.Fatalf("ListFoundSetups: %+v %v", foundRes, err)
	}
	checkNoLeak(foundRes.Msg)

	useRes, err := client.UseFoundSetup(t.Context(), connect.NewRequest(&ariadnev1.UseFoundSetupRequest{
		Id: "openai-compatible:OPENAI_API_KEY",
	}))
	if err != nil {
		t.Fatalf("UseFoundSetup: %v", err)
	}
	if len(useRes.Msg.GetProblems()) != 0 || len(useRes.Msg.GetSettings().GetModels()) != 1 {
		t.Fatalf("unexpected UseFoundSetup result: %+v", useRes.Msg)
	}
	checkNoLeak(useRes.Msg)

	foundAfter, err := client.ListFoundSetups(t.Context(), connect.NewRequest(&ariadnev1.ListFoundSetupsRequest{}))
	if err != nil || len(foundAfter.Msg.GetFound()) != 0 {
		t.Fatalf("expected found setups to be empty after use: %+v %v", foundAfter, err)
	}
}
