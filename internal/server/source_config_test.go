package server

import (
	"context"
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/eventstore"
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

func TestProjectSourcesAndReferencesHTTPMatchLegacy(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(file, []byte("reference"), 0600); err != nil {
		t.Fatal(err)
	}
	part := func(path string) object { return object{"kind": "local-directory", "settings": object{"path": path}} }
	requests := []apiRequest{
		{Method: "POST", Path: "/api/projects", Body: object{"name": "本地项目", "source": part(root), "references": []any{part(file)}}},
		{Method: "POST", Path: "/api/projects", Body: object{"message": "先看看这个项目"}},
		{Method: "POST", Path: "/api/projects", Body: object{"name": "invalid", "source": part(file)}},
		{Method: "POST", Path: "/api/projects", Body: object{"name": "invalid", "source": part(filepath.Join(root, "missing"))}},
		{Method: "POST", Path: "/api/projects", Body: object{"name": "invalid", "source": object{"kind": "missing", "settings": object{}}}},
		{Method: "POST", Path: "/api/projects", Body: object{"name": "invalid", "references": object{}}},
		{Method: "POST", Path: "/api/projects/prj_a/references", Body: object{"reference": part(file)}},
		{Method: "POST", Path: "/api/projects/prj_a/references", Body: object{"reference": part(root)}},
		{Method: "DELETE", Path: "/api/projects/prj_a/references/ref_seed"},
	}
	seed := []eventstore.NewEvent{
		{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)},
		{Type: "project.references.set", ProjectID: "prj_a", Data: json.RawMessage(`{"references":[{"id":"ref_seed","name":"seed","kind":"local-directory","settings":{"path":"old"}}]}`)},
	}
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		command := legacytest.Command(t, bun, "packages/server/src/testing/go-api-fixture.ts")
		command.Dir = filepath.Join("..", "..")
		command.Stdin = strings.NewReader(encoded(object{"settings": true, "local": true, "seed": seed, "sql": "SELECT 1", "requests": requests}))
		data, err := command.CombinedOutput()
		if err != nil || json.Unmarshal(data, &expected) != nil {
			t.Fatalf("legacy %v\n%s", err, data)
		}
	} else if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
		t.Fatal(err)
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
	for index, item := range requests {
		request := httptest.NewRequest(item.Method, item.Path, strings.NewReader(encoded(item.Body)))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		actual := object{"status": response.Code, "body": body}
		if expected != nil && encoded(canonical(actual)) != encoded(canonical(expected[index])) {
			t.Fatalf("%s %s\nGo %s\nTS %s", item.Method, item.Path, encoded(actual), encoded(expected[index]))
		}
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
	sourceID := ""
	for _, token := range []any{"source-private-token", "", nil} {
		entry := object{"kind": "secure", "settings": object{"token": token, "path": "repository"}}
		if sourceID != "" {
			entry["id"] = sourceID
		}
		body := object{"sources": []any{entry}}
		request := httptest.NewRequest("PUT", "/api/projects/prj_a/sources", strings.NewReader(encoded(body)))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != 200 || strings.Contains(response.Body.String(), "source-private-token") {
			t.Fatal(response.Code, response.Body.String())
		}
		if sourceID == "" {
			var parsed struct {
				Project struct {
					FileSources struct {
						PrimaryID string `json:"primaryId"`
					} `json:"fileSources"`
				} `json:"project"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &parsed); err != nil || parsed.Project.FileSources.PrimaryID == "" {
				t.Fatalf("missing file source id: %v %s", err, response.Body.String())
			}
			sourceID = parsed.Project.FileSources.PrimaryID
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
	var before int64
	sourceID := ""
	for i := range 2 {
		entry := object{"kind": "remote", "settings": object{"path": "/repository"}, "hosting": object{"kind": "host", "settings": object{"repository": "owner/project"}}}
		if sourceID != "" {
			entry["id"] = sourceID
		}
		request := httptest.NewRequest("PUT", "/api/projects/prj_a/sources", strings.NewReader(encoded(object{"sources": []any{entry}})))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		if sourceID == "" {
			var parsed struct {
				Project struct {
					FileSources struct {
						PrimaryID string `json:"primaryId"`
					} `json:"fileSources"`
				} `json:"project"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &parsed); err != nil || parsed.Project.FileSources.PrimaryID == "" {
				t.Fatalf("missing file source id: %v %s", err, response.Body.String())
			}
			sourceID = parsed.Project.FileSources.PrimaryID
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
