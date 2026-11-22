package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/settings"
	"github.com/daidr/Ariadne/internal/sources"
	"github.com/daidr/Ariadne/sdk"
)

func TestNewSourceDefaultsRespectOverridesAndSavedSources(t *testing.T) {
	registry := sources.New()
	directory := t.TempDir()
	_, err := registry.Register(sdk.ContextProvider{
		Info: sdk.ContextProviderInfo{Kind: "fixture", Settings: sdk.SettingsSchema{Properties: map[string]sdk.SettingField{
			"directory": {Type: "string", Directory: true},
			"token":     {Type: "string", Secret: true},
		}}},
		NewSourceDefaults: func() map[string]any { return object{"directory": directory, "token": "private"} },
		Connect:           func(context.Context, map[string]any) (sdk.Capabilities, error) { return sdk.Capabilities{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	api := &API{options: Options{Sources: registry}}
	makePart := func(settings object) sources.Part { return sources.Part{Kind: "fixture", Settings: settings} }
	first := makePart(object{})
	if err := api.prepareSource(&first, nil); err != nil || first.Settings["directory"] != directory || first.Settings["token"] != nil {
		t.Fatal("invalid new source defaults", first, err)
	}
	other := t.TempDir()
	override := makePart(object{"directory": other})
	if err := api.prepareSource(&override, nil); err != nil || override.Settings["directory"] != other {
		t.Fatal("ignored project directory", override, err)
	}
	clear := makePart(object{"directory": ""})
	if err := api.prepareSource(&clear, nil); err != nil || clear.Settings["directory"] != nil {
		t.Fatal("explicit blank was replaced by the default", clear, err)
	}
	directory = t.TempDir()
	existing := makePart(object{"directory": other})
	if err := api.prepareSource(&existing, &override); err != nil || existing.Settings["directory"] != other {
		t.Fatal("changing plugin defaults moved a saved project", existing, err)
	}
}

func TestProjectSourcesAndReferencesConnect(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(file, []byte("reference"), 0600); err != nil {
		t.Fatal(err)
	}
	seed := []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)},
		{Type: "project.references.set", ProjectID: "prj_a", Data: json.RawMessage(`{"references":[{"id":"ref_seed","name":"seed","kind":"local-directory","settings":{"path":"old"}}]}`)},
	}
	now := func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := sources.New()
	_, err = registry.Register(local.DirectoryProvider(t.TempDir(), nil, true))
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, Sources: registry, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	client := ariadnev1connect.NewProjectServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)
	localName := "本地项目"
	if _, err := client.CreateProject(t.Context(), connect.NewRequest(&ariadnev1.CreateProjectRequest{
		Name:   &localName,
		Source: &ariadnev1.ProjectSource{Kind: "local-directory", Settings: mustStruct(t, object{"path": root})},
		References: []*ariadnev1.ProjectSourcePart{
			{Kind: "local-directory", Settings: mustStruct(t, object{"path": file})},
		},
	})); err != nil {
		t.Fatalf("CreateProject with local source and file reference failed: %v", err)
	}
	if _, err := client.CreateProject(t.Context(), connect.NewRequest(&ariadnev1.CreateProjectRequest{
		Message: "先看看这个项目",
	})); err != nil {
		t.Fatalf("CreateProject from message failed: %v", err)
	}
	invalidName := "invalid"
	for _, src := range []*ariadnev1.ProjectSource{
		{Kind: "local-directory", Settings: mustStruct(t, object{"path": file})},
		{Kind: "local-directory", Settings: mustStruct(t, object{"path": filepath.Join(root, "missing")})},
		{Kind: "missing", Settings: mustStruct(t, object{})},
	} {
		if _, err := client.CreateProject(t.Context(), connect.NewRequest(&ariadnev1.CreateProjectRequest{
			Name:   &invalidName,
			Source: src,
		})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("invalid source %s code = %v, want CodeInvalidArgument", src.Kind, connect.CodeOf(err))
		}
	}
	if _, err := client.AddProjectReference(t.Context(), connect.NewRequest(&ariadnev1.AddProjectReferenceRequest{
		ProjectId: "prj_a",
		Reference: &ariadnev1.ProjectSourcePart{Kind: "local-directory", Settings: mustStruct(t, object{"path": file})},
	})); err != nil {
		t.Fatalf("AddProjectReference(file) failed: %v", err)
	}
	if _, err := client.AddProjectReference(t.Context(), connect.NewRequest(&ariadnev1.AddProjectReferenceRequest{
		ProjectId: "prj_a",
		Reference: &ariadnev1.ProjectSourcePart{Kind: "local-directory", Settings: mustStruct(t, object{"path": root})},
	})); err != nil {
		t.Fatalf("AddProjectReference(root) failed: %v", err)
	}
	if _, err := client.RemoveProjectReference(t.Context(), connect.NewRequest(&ariadnev1.RemoveProjectReferenceRequest{
		ProjectId:   "prj_a",
		ReferenceId: "ref_seed",
	})); err != nil {
		t.Fatalf("RemoveProjectReference failed: %v", err)
	}
}

func TestSourceSettingsKeepSecretsOutOfProjectAndEventLog(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)}}); err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := sources.New()
	received := []string{}
	_, err = registry.Register(sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "secure", Settings: sdk.SettingsSchema{Type: "object", Properties: map[string]sdk.SettingField{"token": {Type: "string", Secret: true}}}}, Connect: func(_ context.Context, fields map[string]any) (sdk.Capabilities, error) {
		value, _ := fields["token"].(string)
		received = append(received, value)
		return sdk.Capabilities{Workspace: &sdk.WorkspaceCapability{Open: func(context.Context, sdk.WorkspaceRequest) (sdk.Workspace, error) { return nil, nil }}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	client := ariadnev1connect.NewProjectServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)
	sourceID := ""
	for _, token := range []any{"source-private-token", "", nil} {
		input := &ariadnev1.FileSourceInput{
			Kind:     "secure",
			Settings: mustStruct(t, object{"token": token, "path": "repository"}),
		}
		if sourceID != "" {
			id := sourceID
			input.Id = &id
		}
		resp, err := client.SetFileSources(t.Context(), connect.NewRequest(&ariadnev1.SetFileSourcesRequest{
			ProjectId: "prj_a",
			Sources:   []*ariadnev1.FileSourceInput{input},
		}))
		if err != nil {
			t.Fatalf("SetFileSources failed: %v", err)
		}
		raw, err := proto.Marshal(resp.Msg)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "source-private-token") {
			t.Fatal("source secret leaked in SetFileSources response")
		}
		if sourceID == "" {
			sourceID = resp.Msg.GetProject().GetFileSources().GetPrimaryId()
			if sourceID == "" {
				t.Fatalf("missing file source id in %+v", resp.Msg.GetProject())
			}
		}
	}
	if encoded(received) != `["source-private-token","source-private-token",""]` {
		t.Fatal(received)
	}
	events, err := repo.Store.Read(t.Context(), eventstore.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded(events), "source-private-token") {
		t.Fatal("source key recorded in durable events")
	}
	if len(store.SourceSecrets("prj_a")) != 0 {
		t.Fatal("cleared source key retained")
	}
}

func TestUnchangedHostedSourceDoesNotAppendEvents(t *testing.T) {
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)}}); err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	srv := httptest.NewServer(api)
	defer srv.Close()

	client := ariadnev1connect.NewProjectServiceClient(
		srv.Client(),
		srv.URL+rpcPrefix,
		connect.WithInterceptors(bearerClientInterceptor("test-token")),
	)
	var before int64
	sourceID := ""
	for i := range 2 {
		input := &ariadnev1.FileSourceInput{
			Kind:     "remote",
			Settings: mustStruct(t, object{"path": "/repository"}),
			Hosting: &ariadnev1.ProjectSourcePart{
				Kind:     "host",
				Settings: mustStruct(t, object{"repository": "owner/project"}),
			},
		}
		if sourceID != "" {
			id := sourceID
			input.Id = &id
		}
		resp, err := client.SetFileSources(t.Context(), connect.NewRequest(&ariadnev1.SetFileSourcesRequest{
			ProjectId: "prj_a",
			Sources:   []*ariadnev1.FileSourceInput{input},
		}))
		if err != nil {
			t.Fatalf("SetFileSources(%d) failed: %v", i, err)
		}
		if sourceID == "" {
			sourceID = resp.Msg.GetProject().GetFileSources().GetPrimaryId()
			if sourceID == "" {
				t.Fatalf("missing file source id in %+v", resp.Msg.GetProject())
			}
		}
		seq, err := repo.Store.LastSeq(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			before = seq
		} else if seq != before {
			t.Fatal("unchanged source recorded again", before, seq)
		}
	}
}
