package realtime

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func appendLargeOutput(handle LiveHandle) { handle.Output(strings.Repeat("x", 32*1024*1024)) }
func TestLiveOutputLimitReleasesTheOriginalLargeChunk(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	state := NewState(hub)
	defer state.Close()
	live := NewLive(state, LiveOptions{Interval: time.Hour})
	defer live.Close()
	handle, err := live.Start(LiveCommand{ProjectID: "p", ConversationID: "c", Command: "build", CWD: "."})
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	appendLargeOutput(handle)
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > before.HeapAlloc+8*1024*1024 {
		t.Fatalf("bounded live tail retained source chunk: %d extra bytes", after.HeapAlloc-before.HeapAlloc)
	}
	runtime.KeepAlive(handle)
}
