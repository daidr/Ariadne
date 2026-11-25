package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/core"
)

func TestProjectExportHTTPRequiresAuthentication(t *testing.T) {
	t.Parallel()
	no := false
	requests := []apiRequest{{Method: "GET", Path: "/api/projects/prj_a/export", Auth: &no}, {Method: "GET", Path: "/api/projects/prj_a/export"}, {Method: "GET", Path: "/api/projects/missing/export"}}
	hash := sha256.Sum256([]byte{0, 255, 8})
	sql := fmt.Sprintf(`INSERT INTO files VALUES ('fil_export','prj_a','image/png',3,'%s',x'00ff08',0)`, hex.EncodeToString(hash[:]))
	now := func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	repo, err := core.Open(t.Context(), core.Options{Path: ":memory:", Now: now})
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
	wantCodes := []int{401, 200, 404}
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
		if response.Code != wantCodes[i] {
			t.Fatalf("request %d %s status = %d, want %d", i, item.Path, response.Code, wantCodes[i])
		}
		if response.Code == 200 && response.Header().Get("Content-Disposition") != `attachment; filename="ariadne-prj_a.json"` {
			t.Fatal("export lost download filename")
		}
	}
}
