package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/core"
	"github.com/daidr/Ariadne/sdk"
)

func TestBackgroundCommandHTTP(t *testing.T) {
	no := false
	requests := []apiRequest{{Method: "GET", Path: "/api/threads/thread/commands/cmd-1"}, {Method: "GET", Path: "/api/threads/other/commands/cmd-1"}, {Method: "POST", Path: "/api/threads/thread/commands/cmd-1/stop", Auth: &no}, {Method: "GET", Path: "/api/threads/thread/commands/cmd-1"}, {Method: "POST", Path: "/api/threads/thread/commands/cmd-1/stop"}, {Method: "GET", Path: "/api/threads/thread/commands/cmd-1"}, {Method: "POST", Path: "/api/threads/thread/commands/missing/stop"}}
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
	wantCodes := []int{200, 404, 401, 200, 200, 200, 404}
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
		if response.Code != wantCodes[i] {
			t.Fatalf("request %d %s status = %d, want %d (body=%s)", i, item.Path, response.Code, wantCodes[i], response.Body.String())
		}
	}
}
