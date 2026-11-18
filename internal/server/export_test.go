package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/daidr/Ariadne/internal/legacytest"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
)

func TestProjectExportHTTPMatchesLegacyAndRequiresAuthentication(t *testing.T) {
	no := false
	requests := []apiRequest{{Method: "GET", Path: "/api/projects/prj_a/export", Auth: &no}, {Method: "GET", Path: "/api/projects/prj_a/export"}, {Method: "GET", Path: "/api/projects/missing/export"}}
	hash := sha256.Sum256([]byte{0, 255, 8})
	sql := fmt.Sprintf(`INSERT INTO files VALUES ('fil_export','prj_a','image/png',3,'%s',x'00ff08',0)`, hex.EncodeToString(hash[:]))
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		cmd := legacytest.Command(t, bun, "packages/server/src/testing/go-api-fixture.ts")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Stdin = strings.NewReader(encoded(object{"seed": workspaceHTTPSeed(), "sql": sql, "requests": requests}))
		data, err := cmd.CombinedOutput()
		if err != nil || json.Unmarshal(data, &expected) != nil {
			t.Fatalf("legacy %v %s", err, data)
		}
	} else if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
		t.Fatal(err)
	}
	now := func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "export.sqlite"), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.Store.Append(t.Context(), workspaceHTTPSeed()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.DB().ExecContext(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
	api, err := New(Options{Repository: repo, Token: "test-token", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	for i, item := range requests {
		request := httptest.NewRequest(item.Method, item.Path, nil)
		if item.Auth == nil || *item.Auth {
			request.Header.Set("Authorization", "Bearer test-token")
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err, response.Body.String())
		}
		actual := object{"status": response.Code, "body": body}
		if expected != nil && encoded(canonical(actual)) != encoded(canonical(expected[i])) {
			t.Fatalf("export differs\nGo %s\nTS %s", encoded(actual), encoded(expected[i]))
		}
		if response.Code == 200 && response.Header().Get("Content-Disposition") != `attachment; filename="ariadne-prj_a.json"` {
			t.Fatal("export lost download filename")
		}
	}
}
