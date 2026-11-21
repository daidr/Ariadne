package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/internal/local"
	"github.com/daidr/Ariadne/internal/sources"
)

func TestDirectoryPickerHTTP(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a10", "a2", "A1", ".hidden", "notes"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	requests := []apiRequest{}
	requests = append(requests, apiRequest{Method: "GET", Path: "/api/source-kinds"})
	for _, path := range []string{root, filepath.Join(root, "a"), root + string(filepath.Separator) + ".", filepath.Join(root, "not-a-directory") + string(filepath.Separator), filepath.Join(root, "note")} {
		requests = append(requests, apiRequest{Method: "GET", Path: "/api/directories?path=" + url.QueryEscape(path)})
		requests = append(requests, apiRequest{Method: "POST", Path: "/api/source-kinds/local-directory/browse", Body: object{"path": path, "settings": object{}}})
	}
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	registry := sources.New()
	_, err = registry.Register(local.DirectoryProvider(t.TempDir(), nil, true))
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Sources: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	for _, item := range requests {
		request := httptest.NewRequest(item.Method, item.Path, strings.NewReader(encoded(item.Body)))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 {
			t.Fatalf("%s returned %d: %s", item.Path, response.Code, response.Body.String())
		}
	}
}
