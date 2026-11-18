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

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/sdk"
)

func TestBackgroundCommandHTTPMatchesLegacy(t *testing.T) {
	no := false
	requests := []apiRequest{{Method: "GET", Path: "/api/threads/thread/commands/cmd-1"}, {Method: "GET", Path: "/api/threads/other/commands/cmd-1"}, {Method: "POST", Path: "/api/threads/thread/commands/cmd-1/stop", Auth: &no}, {Method: "GET", Path: "/api/threads/thread/commands/cmd-1"}, {Method: "POST", Path: "/api/threads/thread/commands/cmd-1/stop"}, {Method: "GET", Path: "/api/threads/thread/commands/cmd-1"}, {Method: "POST", Path: "/api/threads/thread/commands/missing/stop"}}
	var expected []any
	if bun, err := exec.LookPath("bun"); err == nil {
		cmd := legacytest.Command(t, bun, "packages/server/src/testing/go-api-fixture.ts")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Stdin = strings.NewReader(encoded(object{"background": true, "seed": []any{}, "sql": "SELECT 1", "requests": requests}))
		data, err := cmd.CombinedOutput()
		if err != nil || json.Unmarshal(data, &expected) != nil {
			t.Fatalf("legacy %v\n%s", err, data)
		}
	} else if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
		t.Fatal(err)
	}
	repo, err := core.Open(t.Context(), core.Options{Path: filepath.Join(t.TempDir(), "data.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	commands := background.New(background.Options{Now: func() time.Time { return time.UnixMilli(1_800_000_000_000) }})
	defer commands.Close(context.Background())
	started := make(chan struct{})
	if _, err := commands.Start("thread", "serve", func(ctx context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
		options.OnOutput("Local: http://localhost:5173/\nprogress 10%\rready\n", "stdout")
		close(started)
		<-ctx.Done()
		return sdk.ExecResult{ExitCode: 130}, nil
	}, background.StartOptions{Local: true}); err != nil {
		t.Fatal(err)
	}
	<-started
	api, err := New(Options{Repository: repo, Token: "test-token", Background: commands})
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
			t.Fatal(err)
		}
		actual := object{"status": response.Code, "body": body}
		if expected != nil && encoded(actual) != encoded(expected[i]) {
			t.Fatalf("request %s differs\nGo %s\nTS %s", item.Path, encoded(actual), encoded(expected[i]))
		}
	}
}
