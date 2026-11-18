package background

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

func TestStopThreadTimeoutDoesNotForgetRunningProcess(t *testing.T) {
	commands := New(Options{})
	finish, entered := make(chan struct{}), make(chan struct{})
	defer func() { close(finish); closeCommands(t, commands) }()
	job, err := commands.Start("thread", "slow shutdown", func(ctx context.Context, _ sdk.ExecOptions) (sdk.ExecResult, error) {
		close(entered)
		<-finish
		return sdk.ExecResult{}, nil
	}, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := commands.StopThread(ctx, "thread", false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown timeout hidden", err)
	}
	jobs := commands.List("thread")
	if len(jobs) != 1 || jobs[0].ID != job.ID || jobs[0].Result != nil {
		t.Fatal("running process was forgotten")
	}
}
func TestWaitCancellationAndCapacityAreBounded(t *testing.T) {
	commands := New(Options{})
	defer closeCommands(t, commands)
	job, err := commands.Start("thread", "waiting", func(ctx context.Context, _ sdk.ExecOptions) (sdk.ExecResult, error) {
		<-ctx.Done()
		return sdk.ExecResult{}, ctx.Err()
	}, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := commands.Wait(ctx, "thread", job.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("wait swallowed cancellation", err)
	}
	commands.mu.Lock()
	commands.waiters = 1024
	commands.mu.Unlock()
	if _, err := commands.Wait(t.Context(), "thread", job.ID); err == nil {
		t.Fatal("unbounded waiters")
	}
	commands.mu.Lock()
	commands.waiters = 0
	commands.mu.Unlock()
}
