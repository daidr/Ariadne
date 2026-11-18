package modelhttp

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type bytesReader struct{ io.Reader }

func (reader bytesReader) Read(buffer []byte) (int, error) {
	return reader.Reader.Read(buffer[:min(len(buffer), 1)])
}

func TestSSEChunkingCommentsMultilineAndFinalField(t *testing.T) {
	var events []Event
	err := ReadSSE(bytesReader{strings.NewReader(": alive\r\nevent: text\r\ndata: 中文 🧵\r\ndata: second\r\n\r\nid: ignored\ndata: final")}, func(event Event) bool { events = append(events, event); return true })
	want := []Event{{Comment: true}, {Name: "text", Data: "中文 🧵\nsecond"}, {Data: "final"}}
	if err != nil || !reflect.DeepEqual(events, want) {
		t.Fatalf("events: %+v, %v", events, err)
	}
}

func TestSSEEventLimitAndConsumerStop(t *testing.T) {
	err := ReadSSE(strings.NewReader("data: "+strings.Repeat("x", MaxEventBytes)), func(Event) bool { return true })
	if err == nil {
		t.Fatal("accepted unbounded SSE line")
	}
	count := 0
	if err := ReadSSE(strings.NewReader("data: one\n\ndata: two\n\n"), func(Event) bool { count++; return false }); err != nil || count != 1 {
		t.Fatalf("consumer stop: %d, %v", count, err)
	}
}

func TestRetryDelaysAndCancellation(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		header string
		want   int64
	}{{"0", 0}, {"2.5", 2500}, {"-3", 0}, {"1e100", math.MaxInt64}} {
		response := &http.Response{Header: http.Header{"Retry-After": []string{test.header}}}
		got := RetryAfter(response, now)
		if got == nil || *got != test.want {
			t.Fatalf("retry %q: %v", test.header, got)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if TransportError(ctx, errors.New("cancelled"), true) != nil {
		t.Fatal("cancellation became a retryable error")
	}
	if part := TransportError(t.Context(), io.ErrUnexpectedEOF, true); part == nil || !part.Retryable {
		t.Fatal("broken stream cannot retry")
	}
}
