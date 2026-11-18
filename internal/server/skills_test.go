package server

import (
	"encoding/json"
	"github.com/daidr/Ariadne/internal/legacytest"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daidr/Ariadne/internal/core"
)

func TestConversationSkillsHTTPMatchesLegacy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte("# Review changes"), 0600); err != nil {
		t.Fatal(err)
	}
	no := false
	requests := []apiRequest{{Method: "GET", Path: "/api/conversations/coo_a/skills", Auth: &no}, {Method: "GET", Path: "/api/conversations/coo_a/skills"}, {Method: "GET", Path: "/api/conversations/missing/skills"}}
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		input := object{"seed": traceSeed()[:1], "sql": "SELECT 1", "requests": requests, "skillsDirs": []string{dir}}
		cmd := legacytest.Command(t, bun, "packages/server/src/testing/go-api-fixture.ts")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Stdin = strings.NewReader(encoded(input))
		output, err := cmd.CombinedOutput()
		if err != nil || json.Unmarshal(output, &expected) != nil {
			t.Fatal(err, string(output))
		}
	} else if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
		t.Fatal(err)
	}
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
		actual := object{"status": response.Code, "body": body}
		if expected != nil && encoded(canonical(actual)) != encoded(canonical(expected[i])) {
			t.Fatalf("%s\nGo %s\nTS %s", item.Path, encoded(actual), encoded(expected[i]))
		}
		if i == 0 && response.Code != 401 || i == 1 && !strings.Contains(response.Body.String(), "Review changes") || i == 2 && response.Code != 404 {
			t.Fatal("skills route result", response.Code, response.Body.String())
		}
	}
}
