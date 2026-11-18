package hostinghttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func makeClient(t *testing.T, address string) *Client {
	t.Helper()
	token := "private-token"
	client, err := New(Options{Name: "Host", Base: address, Token: func(context.Context) (*string, error) { return &token, nil }, Authorize: func(header http.Header, token string) { header.Set("Authorization", "Bearer "+token) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}
func TestRedirectCredentialsAreConfinedToAPIOrigin(t *testing.T) {
	var api *httptest.Server
	var downloads atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials reached log host")
		}
		http.Redirect(w, r, api.URL+"/returned", 302)
	}))
	defer external.Close()
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			if r.Header.Get("Authorization") != "Bearer private-token" {
				t.Error("missing API credentials")
			}
			http.Redirect(w, r, external.URL+"/log", 302)
		case "/same":
			http.Redirect(w, r, "/final", 302)
		case "/final":
			if r.Header.Get("Authorization") != "Bearer private-token" {
				t.Error("same-origin redirect lost credentials")
			}
			io.WriteString(w, `{"ok":true}`)
		case "/returned":
			if r.Header.Get("Authorization") != "" {
				t.Error("credentials restored after leaving API host")
			}
			io.WriteString(w, "log output")
		}
	}))
	defer api.Close()
	client := makeClient(t, api.URL)
	var result map[string]bool
	if err := client.Request(t.Context(), "GET", "/same", nil, &result); err != nil || !result["ok"] {
		t.Fatalf("same origin: %v %v", result, err)
	}
	if err := client.Request(t.Context(), "GET", "/redirect", nil, &result); err == nil || downloads.Load() != 0 {
		t.Fatalf("API followed external redirect: %v, calls %d", err, downloads.Load())
	}
	var text string
	err := client.Read(t.Context(), "GET", "/redirect", nil, true, func(reader io.Reader) error { data, err := io.ReadAll(reader); text = string(data); return err })
	if err != nil || text != "log output" || downloads.Load() != 1 {
		t.Fatalf("download failed: %q %v", text, err)
	}
}
func TestRedirectWriteIsNeverConvertedOrRepeated(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Redirect(w, r, "/next", 302) }))
	defer server.Close()
	err := makeClient(t, server.URL).Request(t.Context(), "POST", "/create", map[string]string{"title": "one"}, nil)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("write repeated or reported success: %v, calls %d", err, calls.Load())
	}
}
func TestCloseCancelsRequestsAndDoesNotUseCallerCookieJar(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	client := makeClient(t, server.URL)
	finished := make(chan error, 1)
	go func() { finished <- client.Request(t.Context(), "GET", "/wait", nil, nil) }()
	<-started
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("closed request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("request retained after Close")
	}
	if err := client.Request(t.Context(), "GET", "/wait", nil, nil); err == nil {
		t.Fatal("closed client admitted work")
	}
	client.mu.Lock()
	active := client.active
	client.mu.Unlock()
	if active != 0 {
		t.Fatal("retained requests", active)
	}
}
func TestAPIAndErrorResponsesAreBoundedAndRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/error" {
			w.WriteHeader(403)
			io.WriteString(w, "private-token "+strings.Repeat("x", 128*1024))
			return
		}
		io.Copy(w, io.LimitReader(infiniteReader{}, MaxResponse+10))
	}))
	defer server.Close()
	client := makeClient(t, server.URL)
	if err := client.Request(t.Context(), "GET", "/large", nil, nil); err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatal("unbounded API response", err)
	}
	err := client.Request(t.Context(), "GET", "/error", nil, nil)
	if err == nil || strings.Contains(err.Error(), "private-token") || len(err.Error()) > 66*1024 {
		t.Fatal("unbounded or unredacted error")
	}
}

type infiniteReader struct{}

func (infiniteReader) Read(data []byte) (int, error) {
	for i := range data {
		data[i] = 'x'
	}
	return len(data), nil
}
func TestTailRetainsOnlyEndAndCountsUTF16(t *testing.T) {
	for _, test := range []struct {
		input string
		limit int
		want  string
	}{{"甲😀乙", 4, "甲😀乙"}, {"甲😀乙", 3, "[… 1 characters cut …]\n😀乙"}, {"甲😀乙", 2, "[… 2 characters cut …]\n�乙"}, {"\xef\xbb\xbfabc", 3, "abc"}, {string([]byte{'a', 0xff, 'b'}), 3, "a�b"}} {
		got, err := Tail(strings.NewReader(test.input), test.limit)
		if err != nil || got != test.want {
			t.Fatalf("tail %q: %q %v; want %q", test.input, got, err, test.want)
		}
	}
}
func TestRequestCapacityRejectsWithoutQueueing(t *testing.T) {
	client := makeClient(t, "http://127.0.0.1:1")
	var release []func()
	for i := 0; i < 128; i++ {
		_, done, err := client.begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		release = append(release, done)
	}
	if _, _, err := client.begin(t.Context()); err == nil {
		t.Fatal("unbounded active requests")
	}
	for _, done := range release {
		done()
	}
	_, done, err := client.begin(t.Context())
	if err != nil {
		t.Fatal("capacity not released", err)
	}
	done()
}
func TestMemoSingleFlightCancellationExpiryAndClose(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	ready := make(chan struct{})
	memo := NewMemo(30*time.Millisecond, func(ctx context.Context) (*string, error) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-ready:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		value := "secret"
		return &value, nil
	}, func(value *string) bool { return value != nil })
	defer memo.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancelled := make(chan error, 1)
	go func() { _, err := memo.Get(ctx); cancelled <- err }()
	<-started
	var waiters sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 20; i++ {
		waiters.Add(1)
		go func() {
			defer waiters.Done()
			value, err := memo.Get(t.Context())
			if err != nil || value == nil || *value != "secret" {
				failures.Add(1)
			}
		}()
	}
	cancel()
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Fatal("waiter not cancelled", err)
	}
	close(ready)
	waiters.Wait()
	if calls.Load() != 1 || failures.Load() != 0 {
		t.Fatal("lookup not shared", calls.Load(), failures.Load())
	}
	deadline := time.Now().Add(time.Second)
	for {
		memo.mu.Lock()
		released := !memo.valid && memo.value == nil
		memo.mu.Unlock()
		if released {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired secret retained without access")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := memo.Get(t.Context()); err != nil || calls.Load() != 2 {
		t.Fatal("expiry not reloaded", err, calls.Load())
	}
	memo.Close()
	memo.mu.Lock()
	retained := memo.valid || memo.value != nil || memo.pending != nil || memo.timer != nil || memo.load != nil || memo.keep != nil
	memo.mu.Unlock()
	if retained {
		t.Fatal("closed memo retained resources")
	}
	if _, err := memo.Get(t.Context()); err == nil {
		t.Fatal("closed memo reloaded")
	}
}
func TestMemoCloseCancelsOwnedLookupAndMissingValuesRetry(t *testing.T) {
	var calls atomic.Int32
	memo := NewMemo(time.Hour, func(ctx context.Context) (*string, error) { calls.Add(1); return nil, nil }, func(value *string) bool { return value != nil })
	for i := 0; i < 2; i++ {
		if _, err := memo.Get(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("missing token cached", calls.Load())
	}
	memo.Close()
	started := make(chan struct{})
	blocked := NewMemo(time.Hour, func(ctx context.Context) (string, error) { close(started); <-ctx.Done(); return "", ctx.Err() }, nil)
	finished := make(chan error, 1)
	go func() { _, err := blocked.Get(t.Context()); finished <- err }()
	<-started
	blocked.Close()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal("owned lookup survived close", err)
	}
}
func TestURLComponentsMatchJavaScript(t *testing.T) {
	if got := Component("a b/~!*'()中文"); got != "a%20b%2F~!*'()%E4%B8%AD%E6%96%87" {
		t.Fatal(got)
	}
	if got := FormComponent("a b/~!*'()中文"); got != "a+b%2F%7E%21*%27%28%29%E4%B8%AD%E6%96%87" {
		t.Fatal(got)
	}
	for _, address := range []string{"ftp://host", "https://token@host", "https://host?token=key", "https://host/#x"} {
		if _, err := New(Options{Base: address}); err == nil {
			t.Fatal(fmt.Sprintf("accepted %s", address))
		}
	}
}
