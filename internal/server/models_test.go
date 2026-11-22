package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/providers"
	"github.com/daidr/Ariadne/internal/settings"
)

func mustStruct(t *testing.T, values map[string]any) *structpb.Struct {
	t.Helper()
	out, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	return out
}

func TestModelSettingsConnectAndRealProviderCheck(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer private" {
			t.Error("wrong model check request", r.URL.Path, r.Header)
		}
		var body object
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] == "refused" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":{"message":"unauthorized"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
	}))
	defer service.Close()

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
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, Models: modelReg})
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
	checkNoSecretLeak := func(msg proto.Message) {
		t.Helper()
		raw, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "private") {
			t.Fatal("API key leaked to settings response")
		}
	}

	initial, err := client.GetModelSettings(t.Context(), connect.NewRequest(&ariadnev1.GetModelSettingsRequest{}))
	if err != nil {
		t.Fatalf("GetModelSettings: %v", err)
	}
	if len(initial.Msg.GetSettings().GetKinds()) == 0 {
		t.Fatal("expected built-in model kinds")
	}
	checkNoSecretLeak(initial.Msg)

	saveReq := func(model string) *ariadnev1.SaveModelSettingsRequest {
		modelID := "m"
		return &ariadnev1.SaveModelSettingsRequest{
			Providers: []*ariadnev1.ModelProviderInput{{
				Id:       "p",
				Kind:     "openai-compatible",
				Name:     "主力",
				Settings: mustStruct(t, object{"baseUrl": service.URL + "/v1", "apiKey": "private"}),
			}},
			Models: []*ariadnev1.ModelEntry{{
				Id:       "m",
				Provider: "p",
				Model:    model,
			}},
			Roles: &ariadnev1.ModelRoles{
				Thread: &ariadnev1.RoleConfig{Model: &modelID},
			},
		}
	}

	okRes, err := client.SaveModelSettings(t.Context(), connect.NewRequest(saveReq("working")))
	if err != nil {
		t.Fatalf("SaveModelSettings(working): %v", err)
	}
	if len(okRes.Msg.GetProblems()) != 0 {
		t.Fatalf("expected no problems for working model, got %+v", okRes.Msg.GetProblems())
	}
	checkNoSecretLeak(okRes.Msg)

	refusedRes, err := client.SaveModelSettings(t.Context(), connect.NewRequest(saveReq("refused")))
	if err != nil {
		t.Fatalf("SaveModelSettings(refused): %v", err)
	}
	if len(refusedRes.Msg.GetProblems()) != 1 || refusedRes.Msg.GetProblems()[0].GetProblem().GetKey() != "error.modelCheck" {
		t.Fatalf("expected error.modelCheck problem, got %+v", refusedRes.Msg.GetProblems())
	}
	checkNoSecretLeak(refusedRes.Msg)

	after, err := client.GetModelSettings(t.Context(), connect.NewRequest(&ariadnev1.GetModelSettingsRequest{}))
	if err != nil {
		t.Fatalf("GetModelSettings(after): %v", err)
	}
	checkNoSecretLeak(after.Msg)
}

func TestSaveModelSettingsProbeTimeout(t *testing.T) {
	originalTimeout := modelProbeTimeout
	modelProbeTimeout = 150 * time.Millisecond
	t.Cleanup(func() { modelProbeTimeout = originalTimeout })

	release := make(chan struct{})
	slowService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer slowService.Close()
	defer close(release)

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
	for _, provider := range providers.HTTPProviders(slowService.Client()) {
		remove, err := modelReg.Register(provider)
		if err != nil {
			t.Fatal(err)
		}
		defer remove()
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, Models: modelReg})
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

	start := time.Now()
	res, err := client.SaveModelSettings(t.Context(), connect.NewRequest(&ariadnev1.SaveModelSettingsRequest{
		Providers: []*ariadnev1.ModelProviderInput{{
			Id:       "p",
			Kind:     "openai-compatible",
			Name:     "慢接口",
			Settings: mustStruct(t, object{"baseUrl": slowService.URL + "/v1", "apiKey": "private"}),
		}},
		Models: []*ariadnev1.ModelEntry{{
			Id:       "m",
			Provider: "p",
			Model:    "slow-model",
		}},
	}))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("SaveModelSettings(slow): %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("SaveModelSettings blocked too long (%v) despite modelProbeTimeout", elapsed)
	}
	if len(res.Msg.GetProblems()) != 1 || res.Msg.GetProblems()[0].GetProblem().GetKey() != "error.modelCheckTimeout" {
		t.Fatalf("expected error.modelCheckTimeout, got %+v", res.Msg.GetProblems())
	}
}
