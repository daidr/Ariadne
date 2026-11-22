package server

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/daidr/Ariadne/internal/background"
	"github.com/daidr/Ariadne/internal/core"
	ariadnev1 "github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1"
	"github.com/daidr/Ariadne/internal/gen/proto/ariadne/v1/ariadnev1connect"
	"github.com/daidr/Ariadne/sdk"
)

func TestBackgroundCommandRPC(t *testing.T) {
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
	srv := httptest.NewServer(api)
	defer srv.Close()

	client := ariadnev1connect.NewThreadServiceClient(srv.Client(), srv.URL+rpcPrefix, connect.WithInterceptors(bearerClientInterceptor("test-token")))
	unauth := ariadnev1connect.NewThreadServiceClient(srv.Client(), srv.URL+rpcPrefix)

	out, err := client.GetCommandOutput(t.Context(), connect.NewRequest(&ariadnev1.GetCommandOutputRequest{ThreadId: "thread", CommandId: "cmd-1"}))
	if err != nil || out.Msg.Command == nil || out.Msg.Command.Id != "cmd-1" || !strings.Contains(out.Msg.Tail, "ready") {
		t.Fatalf("get command output failed: resp=%+v err=%v", out, err)
	}

	_, err = client.GetCommandOutput(t.Context(), connect.NewRequest(&ariadnev1.GetCommandOutputRequest{ThreadId: "other", CommandId: "cmd-1"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected not found for wrong thread, got %v", err)
	}

	_, err = unauth.StopCommand(t.Context(), connect.NewRequest(&ariadnev1.StopCommandRequest{ThreadId: "thread", CommandId: "cmd-1"}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expected unauthenticated, got %v", err)
	}

	stopped, err := client.StopCommand(t.Context(), connect.NewRequest(&ariadnev1.StopCommandRequest{ThreadId: "thread", CommandId: "cmd-1"}))
	if err != nil || stopped.Msg.Command == nil || stopped.Msg.Command.Result == nil || !stopped.Msg.Command.Result.Stopped {
		t.Fatalf("stop command failed: resp=%+v err=%v", stopped, err)
	}

	after, err := client.GetCommandOutput(t.Context(), connect.NewRequest(&ariadnev1.GetCommandOutputRequest{ThreadId: "thread", CommandId: "cmd-1"}))
	if err != nil || after.Msg.Command == nil || after.Msg.Command.Result == nil {
		t.Fatalf("get output after stop failed: resp=%+v err=%v", after, err)
	}

	_, err = client.StopCommand(t.Context(), connect.NewRequest(&ariadnev1.StopCommandRequest{ThreadId: "thread", CommandId: "missing"}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected not found for missing command, got %v", err)
	}
}
