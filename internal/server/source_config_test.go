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
		{Method: "PUT", Path: "/api/projects/prj_a/source", Body: object{"source": part(root)}},
		{Method: "GET", Path: "/api/projects/prj_a/source"},
		{Method: "POST", Path: "/api/projects/prj_a/references", Body: object{"reference": part(file)}},
		{Method: "POST", Path: "/api/projects/prj_a/references", Body: object{"reference": part(root)}},
		{Method: "DELETE", Path: "/api/projects/prj_a/references/ref_seed"},
		{Method: "PUT", Path: "/api/projects/prj_a/source", Body: object{"source": object{"kind": "local-directory", "settings": object{}}}},
		{Method: "PUT", Path: "/api/projects/prj_a/source", Body: object{}},
		{Method: "PUT", Path: "/api/projects/prj_a/source", Body: object{"source": nil}},
		{Method: "GET", Path: "/api/projects/prj_a/source"},
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
		return sdk.Capabilities{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Settings: store, Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	for _, token := range []any{"source-private-token", "", nil} {
		body := object{"source": object{"kind": "secure", "settings": object{"token": token, "path": "repository"}}}
		request := httptest.NewRequest("PUT", "/api/projects/prj_a/source", strings.NewReader(encoded(body)))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != 200 || strings.Contains(response.Body.String(), "source-private-token") {
			t.Fatal(response.Code, response.Body.String())
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
	for i := range 2 {
		body := `{"source":{"kind":"remote","settings":{"path":"/repository"},"hosting":{"kind":"host","settings":{"repository":"owner/project"}}}}`
		request := httptest.NewRequest("PUT", "/api/projects/prj_a/source", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
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
