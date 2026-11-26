package server

import (
	"context"
	"iter"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/models"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/sdk"
)

func TestConnectProviderEnvAndProjectModels(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	repo, err := core.Open(ctx, core.Options{Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	store, err := settings.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	var seenListEnv map[string]any
	var seenCheckEnv map[string]any
	modelReg := models.New()
	if _, err := modelReg.Register(sdk.ModelProvider{
		Info: sdk.ModelProviderInfo{
			Kind:    "claude-code",
			Title:   sdk.LocalizedText{"zh-CN": "Claude Code", "en": "Claude Code"},
			Program: true,
			Settings: sdk.SettingsSchema{
				Type: "object",
				Properties: map[string]sdk.SettingField{
					"command": {Type: "string", Default: "claude"},
				},
			},
		},
		Stream: func(_ context.Context, _ map[string]any, _ sdk.Request) iter.Seq[sdk.Part] {
			return func(yield func(sdk.Part) bool) { yield(sdk.Part{Type: "text", Text: "ok"}) }
		},
		Models: func(_ context.Context, cfg map[string]any, _ sdk.ModelListOptions) ([]sdk.ListedModel, error) {
			seenListEnv, _ = cfg["env"].(map[string]any)
			return []sdk.ListedModel{{ID: "default"}, {ID: "sonnet"}}, nil
		},
		Check: func(_ context.Context, cfg map[string]any) (sdk.LocalizedText, error) {
			seenCheckEnv, _ = cfg["env"].(map[string]any)
			return nil, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, Models: modelReg})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	auth := func(req connect.AnyRequest) {
		req.Header().Set("Authorization", "Bearer test-token")
	}

	settingsClient := ariadnev1connect.NewSettingsServiceClient(srv.Client(), srv.URL+rpcPrefix)
	projectClient := ariadnev1connect.NewProjectServiceClient(srv.Client(), srv.URL+rpcPrefix)
	convClient := ariadnev1connect.NewConversationServiceClient(srv.Client(), srv.URL+rpcPrefix)

	// 1. SaveModelSettings stores provider env, passes env to Check, and returns masked names.
	saveReq := connect.NewRequest(&ariadnev1.SaveModelSettingsRequest{
		Providers: []*ariadnev1.ModelProviderInput{
			{
				Id:       "p-cc",
				Kind:     "claude-code",
				Name:     "Claude Code",
				Settings: mustStruct(t, map[string]any{"command": "claude"}),
				Env: []*ariadnev1.SecretPair{
					{Name: "ANTHROPIC_BASE_URL", Value: "https://proxy.example"},
					{Name: "ANTHROPIC_AUTH_TOKEN", Value: "secret-12345"},
				},
			},
		},
		Models: []*ariadnev1.ModelEntry{
			{Id: "m-default", Provider: "p-cc", Model: "default", Efforts: &ariadnev1.StringList{Values: []string{"low", "medium", "high"}}},
			{Id: "m-special", Provider: "p-cc", Model: "sonnet", Efforts: &ariadnev1.StringList{Values: []string{"low", "medium", "high"}}},
		},
		Roles: &ariadnev1.ModelRoles{
			Thread:      &ariadnev1.RoleConfig{Model: new("m-default"), Effort: new("high")},
			Coordinator: &ariadnev1.RoleConfig{},
			Triage:      &ariadnev1.RoleConfig{},
			Research:    &ariadnev1.RoleConfig{},
		},
	})
	auth(saveReq)
	saveResp, err := settingsClient.SaveModelSettings(ctx, saveReq)
	if err != nil {
		t.Fatalf("SaveModelSettings: %v", err)
	}
	if rawResp, err := proto.Marshal(saveResp.Msg); err != nil || strings.Contains(string(rawResp), "secret-12345") {
		t.Fatalf("provider env secret leaked in SaveModelSettingsResponse (err=%v)", err)
	}
	if !slices.Equal(saveResp.Msg.GetSettings().GetProviders()[0].GetEnv(), []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"}) {
		t.Fatalf("returned provider env names = %v", saveResp.Msg.GetSettings().GetProviders()[0].GetEnv())
	}
	if seenCheckEnv["ANTHROPIC_AUTH_TOKEN"] != "secret-12345" {
		t.Fatalf("provider Check env = %#v", seenCheckEnv)
	}

	// 2. ListProviderModels accepts draft env (merging saved secret + new draft entry).
	listReq := connect.NewRequest(&ariadnev1.ListProviderModelsRequest{
		Provider: &ariadnev1.DraftModelProviderInput{
			Id:       "p-cc",
			Kind:     "claude-code",
			Settings: mustStruct(t, map[string]any{"command": "claude"}),
			Env: &ariadnev1.SecretPairList{
				Items: []*ariadnev1.SecretPair{
					{Name: "ANTHROPIC_AUTH_TOKEN", Value: ""},
					{Name: "DRAFT_ENV", Value: "draft-value"},
				},
			},
		},
	})
	auth(listReq)
	if _, err := settingsClient.ListProviderModels(ctx, listReq); err != nil {
		t.Fatalf("ListProviderModels: %v", err)
	}
	if seenListEnv["ANTHROPIC_AUTH_TOKEN"] != "secret-12345" || seenListEnv["DRAFT_ENV"] != "draft-value" {
		t.Fatalf("provider Models draft env = %#v", seenListEnv)
	}

	// 3. Create project & update its project-level model overrides.
	project, err := repo.CreateProject(ctx, new("Special Repo"), core.ProjectStart{})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repo.CreateThread(ctx, project.ID, core.NewThread{Title: "Thread 1"})
	if err != nil {
		t.Fatal(err)
	}

	// Reject unknown model ID.
	badUpdate := connect.NewRequest(&ariadnev1.UpdateProjectRequest{
		ProjectId: project.ID,
		Settings: &ariadnev1.ProjectSettings{
			Models: &ariadnev1.ProjectModelsSettings{
				Thread: &ariadnev1.ProjectModelRoleSettings{Model: new("m-unknown")},
			},
		},
	})
	auth(badUpdate)
	if _, err := projectClient.UpdateProject(ctx, badUpdate); err == nil {
		t.Fatal("expected unknown model ID in project settings to be rejected")
	}

	// Save valid project model overrides.
	goodUpdate := connect.NewRequest(&ariadnev1.UpdateProjectRequest{
		ProjectId: project.ID,
		Settings: &ariadnev1.ProjectSettings{
			Models: &ariadnev1.ProjectModelsSettings{
				Thread:      &ariadnev1.ProjectModelRoleSettings{Model: new("m-special"), Effort: new("medium")},
				Coordinator: &ariadnev1.ProjectModelRoleSettings{Model: new("m-special"), Effort: new("low")},
				Research:    &ariadnev1.ProjectModelRoleSettings{Model: new("m-default"), Effort: new("low")},
			},
		},
	})
	auth(goodUpdate)
	updateResp, err := projectClient.UpdateProject(ctx, goodUpdate)
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	savedModels := updateResp.Msg.GetSettingsView().GetSettings().GetModels()
	if savedModels.GetThread().GetModel() != "m-special" || savedModels.GetThread().GetEffort() != "medium" ||
		savedModels.GetCoordinator().GetModel() != "m-special" || savedModels.GetCoordinator().GetEffort() != "low" ||
		savedModels.GetResearch().GetModel() != "m-default" || savedModels.GetResearch().GetEffort() != "low" {
		t.Fatalf("saved project models = %+v", savedModels)
	}

	// 4. GetConversation reflects the project's overridden models for thread & coordinator.
	threadConvReq := connect.NewRequest(&ariadnev1.GetConversationRequest{ConversationId: thread.ID})
	auth(threadConvReq)
	threadConvResp, err := convClient.GetConversation(ctx, threadConvReq)
	if err != nil {
		t.Fatalf("GetConversation(thread): %v", err)
	}
	if threadConvResp.Msg.GetModel().GetId() != "m-special" || threadConvResp.Msg.GetModel().GetEffort() != "medium" {
		t.Fatalf("thread conversation model = %+v", threadConvResp.Msg.GetModel())
	}

	coordConvReq := connect.NewRequest(&ariadnev1.GetConversationRequest{ConversationId: project.CoordinatorID})
	auth(coordConvReq)
	coordConvResp, err := convClient.GetConversation(ctx, coordConvReq)
	if err != nil {
		t.Fatalf("GetConversation(coordinator): %v", err)
	}
	if coordConvResp.Msg.GetModel().GetId() != "m-special" || coordConvResp.Msg.GetModel().GetEffort() != "low" {
		t.Fatalf("coordinator conversation model = %+v", coordConvResp.Msg.GetModel())
	}
}
