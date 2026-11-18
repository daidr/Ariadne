package background

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daidr/Ariadne/internal/process"
	"github.com/daidr/Ariadne/sdk"
)

func TestBackgroundProcessChild(t *testing.T) {
	if os.Args[len(os.Args)-1] != "--background-child" {
		return
	}
	fmt.Print("READY\n")
	time.Sleep(time.Minute)
}
func TestStopThreadJoinsActualProcessBeforeReleasingWorkspace(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "workspace")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := New(Options{})
	defer closeCommands(t, commands)
	view, err := commands.Start("thread", "test child", func(ctx context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
		return process.Run(ctx, []string{program, "-test.run=^TestBackgroundProcessChild$", "--", "--background-child"}, process.RunOptions{Directory: cwd, Timeout: options.Timeout, ForThread: true, OnOutput: options.OnOutput}), nil
	}, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ready := false
	for !ready && ctx.Err() == nil {
		output, err := commands.Read(ctx, "thread", view.ID, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		ready = output != nil && strings.Contains(output.Text, "READY")
	}
	if !ready {
		t.Fatal("native process did not start")
	}
	if err := commands.StopThread(t.Context(), "thread", false); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cwd, filepath.Join(root, "moved")); err != nil {
		t.Fatal("stopped process still holds workspace", err)
	}
	commands.mu.Lock()
	active, units := commands.active, commands.units
	commands.mu.Unlock()
	if active != 0 || units != 0 {
		t.Fatal("stopped process retained background resources", active, units)
	}
}
