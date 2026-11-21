package background

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

func closeCommands(t *testing.T, commands *Commands) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := commands.Close(ctx); err != nil {
		t.Error(err)
	}
}

func TestBackgroundReadKeepsUnreadPagesAfterExit(t *testing.T) {
	ctx := t.Context()
	commands := New(Options{})
	t.Cleanup(func() { closeCommands(t, commands) })
	const limit = 30_000
	first := strings.Repeat("x", limit-1)
	second := "😀" + strings.Repeat("y", limit-2)
	const third = "build failed\n"
	job, err := commands.Start("thread", "build", func(ctx context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
		options.OnOutput(first+second+third, "stdout")
		return sdk.ExecResult{ExitCode: 1}, nil
	}, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Wait(ctx, "thread", job.ID); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := commands.Read(cancelled, "thread", job.ID, 0, limit); err != context.Canceled {
		t.Fatalf("cancelled read: %v", err)
	}
	for i, expected := range []string{first, second, third, ""} {
		page, err := commands.Read(ctx, "thread", job.ID, 0, limit)
		if err != nil || page == nil {
			t.Fatalf("page %d: %v", i, err)
		}
		if page.Text != expected || page.More != (i < 2) || page.Missed != 0 || page.Command.Result == nil {
			t.Fatalf("page %d: got %d bytes, more %v, missed %d", i, len(page.Text), page.More, page.Missed)
		}
	}
}

func TestBackgroundFlowAndServedURLs(t *testing.T) {
	urls := []string{"  \x1b[32m➜\x1b[39m  Local:   http://localhost:\x1b[1m5173\x1b[22m/\n", "Listening on http://0.0.0.0:3000 (press Ctrl+C)\n", "open http://127.0.0.1:8080/docs/.\n", "proxy /api -> http://localhost:8787\n  Local:   http://localhost:5173/\n", "Local: http://localhost:51", "fetching https://example.com:443/x \n", "Ready http://[::]:5173/\n", "Ready http://[::1]:5173/\n", "Local: http://localhost:5173/\u3000", "http://localhost:123456/\n"}
	chunks := []string{"\x1b[32mLocal: http://localhost:5173/\x1b[0m\n", "10%\r100%\n中文😀\n"}
	commands := New(Options{Now: func() time.Time { return time.UnixMilli(1_800_000_000_000) }})
	defer closeCommands(t, commands)
	started := make(chan sdk.ExecOptions, 1)
	finish := make(chan struct{})
	command, err := commands.Start("thread", "serve", func(ctx context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
		started <- options
		select {
		case <-finish:
			return sdk.ExecResult{ExitCode: 0}, nil
		case <-ctx.Done():
			return sdk.ExecResult{ExitCode: 130}, nil
		}
	}, StartOptions{Local: true, Keep: true, CWD: "sub"})
	if err != nil {
		t.Fatal(err)
	}
	options := <-started
	reads := []any{}
	for _, chunk := range chunks {
		options.OnOutput(chunk, "stdout")
		read, err := commands.Read(t.Context(), "thread", command.ID, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		reads = append(reads, read)
	}
	peek := commands.Peek("thread", command.ID, 0)
	if err := commands.StopThread(t.Context(), "thread", true); err != nil {
		t.Fatal(err)
	}
	kept := commands.TakeKept("thread")
	close(finish)
	if _, err := commands.Wait(t.Context(), "thread", command.ID); err != nil {
		t.Fatal(err)
	}
	ended := commands.List("thread")
	commands.StopThread(t.Context(), "thread", false)
	parsedURLs := []*string{}
	for _, output := range urls {
		parsedURLs = append(parsedURLs, ServedURL(output))
	}
	if len(reads) != len(chunks) || peek == nil || len(kept) != 1 || len(ended) != 1 || len(commands.List("thread")) != 0 || parsedURLs[0] == nil || *parsedURLs[0] != "http://localhost:5173/" {
		t.Fatalf("unexpected background flow state: reads=%d kept=%d ended=%d url=%v", len(reads), len(kept), len(ended), parsedURLs[0])
	}
}
func TestThreadCapacityReaderCancellationStopAndCloseReleaseResources(t *testing.T) {
	commands := New(Options{})
	defer closeCommands(t, commands)
	run := func(ctx context.Context, _ sdk.ExecOptions) (sdk.ExecResult, error) {
		<-ctx.Done()
		return sdk.ExecResult{ExitCode: 130}, nil
	}
	var first Command
	for i := 0; i < MaxRunning; i++ {
		view, err := commands.Start("thread", "wait", run, StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = view
		}
	}
	if _, err := commands.Start("thread", "overflow", run, StartOptions{}); err == nil {
		t.Fatal("unbounded running commands")
	}
	if peek := commands.Peek("other", first.ID, 0); peek != nil {
		t.Fatal("another thread read command")
	}
	ctx, cancel := context.WithCancel(t.Context())
	readDone := make(chan struct{})
	go func() { commands.Read(ctx, "thread", first.ID, time.Minute, 0); close(readDone) }()
	cancel()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("reader did not cancel")
	}
	view, err := commands.Stop(t.Context(), "thread", first.ID)
	if err != nil || view == nil || view.Result == nil || !view.Result.Stopped {
		t.Fatal("stop failed", err)
	}
	if err := commands.StopThread(t.Context(), "thread", false); err != nil {
		t.Fatal(err)
	}
	if len(commands.List("thread")) != 0 {
		t.Fatal("thread deletion retained command")
	}
	commands.mu.Lock()
	retained := commands.units != 0 || commands.active != 0 || commands.waiters != 0
	commands.mu.Unlock()
	if retained {
		t.Fatal("thread deletion retained resources")
	}
	closeCommands(t, commands)
	if _, err := commands.Start("thread", "late", run, StartOptions{}); err == nil {
		t.Fatal("closed manager accepted work")
	}
}
func TestFinishedOutputsExpireWithoutReadAndOldEntriesAreBounded(t *testing.T) {
	var now atomic.Int64
	now.Store(1_800_000_000_000)
	commands := New(Options{Now: func() time.Time { return time.UnixMilli(now.Load()) }, FinishedTTL: 20 * time.Millisecond})
	defer closeCommands(t, commands)
	for i := 0; i < 20; i++ {
		view, err := commands.Start("thread", "once", func(_ context.Context, options sdk.ExecOptions) (sdk.ExecResult, error) {
			options.OnOutput("large output", "stdout")
			return sdk.ExecResult{}, nil
		}, StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		commands.Wait(t.Context(), "thread", view.ID)
	}
	if count := len(commands.List("thread")); count > 8 {
		t.Fatal("finished command limit exceeded", count)
	}
	now.Add(100)
	deadline := time.Now().Add(time.Second)
	for len(commands.List("thread")) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("expired outputs retained without access")
		}
		time.Sleep(time.Millisecond)
	}
	commands.mu.Lock()
	bytes := commands.units
	commands.mu.Unlock()
	if bytes != 0 {
		t.Fatal("expiry retained output")
	}
}
func TestOutputTruncationReadCursorAndGlobalCapacityReleaseArrays(t *testing.T) {
	buffer := outputBuffer{}
	buffer.append(strings.Repeat("a", 3_000_000))
	if buffer.length() != keptOutput || buffer.dropped != 1_000_000 {
		t.Fatal("output truncation changed", buffer.length(), buffer.dropped)
	}
	text, missed, _ := buffer.take(0)
	if len(text) != keptOutput || missed != 1_000_000 {
		t.Fatal("read cursor lost truncation count", missed)
	}
	buffer.append("next")
	if text, missed, _ := buffer.take(0); text != "next" || missed != 0 {
		t.Fatal("incremental read wrong", text, missed)
	}
	buffer.drop(buffer.length())
	if buffer.units != nil || buffer.head != 0 {
		t.Fatal("discarded output retained backing array")
	}
	commands := New(Options{})
	defer closeCommands(t, commands)
	commands.mu.Lock()
	for i := 0; i < 30; i++ {
		id := "cmd-" + strings.Repeat("x", i+1)
		entry := &job{view: Command{ID: id, ThreadID: "thread"}, done: make(chan struct{}), changed: make(chan struct{})}
		entry.output.append(strings.Repeat("x", 1_500_000))
		commands.jobs[id] = entry
		commands.order = append(commands.order, id)
		commands.units += entry.output.length()
	}
	commands.limitOutput()
	units := commands.units
	allocated := 0
	for _, entry := range commands.jobs {
		allocated += cap(entry.output.units)
	}
	commands.mu.Unlock()
	if units > maxOutputUnits || allocated > maxOutputUnits*3 {
		t.Fatal("global output budget does not bound allocations", units, allocated)
	}
}
func TestAdoptedCommandOutputAndCancellationAreOwned(t *testing.T) {
	commands := New(Options{})
	defer closeCommands(t, commands)
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan Execution, 1)
	go func() { <-ctx.Done(); ended <- Execution{Result: sdk.ExecResult{ExitCode: 130}} }()
	view, append, err := commands.Adopt("thread", "already running", ctx, cancel, ended, "before\n", StartOptions{})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	append("after\n")
	output, err := commands.Read(t.Context(), "thread", view.ID, 0, 0)
	if err != nil || output == nil || output.Text != "before\nafter\n" {
		t.Fatal("adopted output lost", err)
	}
	stopped, err := commands.Stop(t.Context(), "thread", view.ID)
	if err != nil || stopped == nil || stopped.Result == nil || !stopped.Result.Stopped {
		t.Fatal("adopted cancellation failed", err)
	}
}
