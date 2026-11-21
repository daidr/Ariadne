package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
)

func TestConversationSkillsHTTP(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte("# Review changes"), 0600); err != nil {
		t.Fatal(err)
	}
	no := false
	requests := []apiRequest{{Method: "GET", Path: "/api/conversations/coo_a/skills", Auth: &no}, {Method: "GET", Path: "/api/conversations/coo_a/skills"}, {Method: "GET", Path: "/api/conversations/missing/skills"}}
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "skills.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), traceSeed()[:1]); err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", SkillsDirs: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	for i, item := range requests {
		r := httptest.NewRequest(item.Method, item.Path, nil)
		if item.Auth == nil || *item.Auth {
			r.Header.Set("Authorization", "Bearer test-token")
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if i == 0 && response.Code != 401 || i == 1 && !strings.Contains(response.Body.String(), "Review changes") || i == 2 && response.Code != 404 {
			t.Fatal("skills route result", response.Code, response.Body.String())
		}
	}
}
