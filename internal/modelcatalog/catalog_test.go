package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/daidr/Ariadne/internal/legacytest"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestCatalog(t *testing.T, options Options) *Catalog {
	t.Helper()
	catalog := New(options)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := catalog.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return catalog
}

func TestModelCatalogMatchesLegacySelection(t *testing.T) {
	raw := `{"reseller":{"models":{"deepseek/chat":{"cost":{"input":0.5,"output":2},"limit":{"output":2048},"modalities":{"input":["image"]},"reasoning":false,"tool_call":false,"release_date":"2026-01-02"},"vendor/bare":{"limit":{"context":16000}}}},"openrouter":{"models":{"deepseek/chat":{"cost":{"input":0.3,"output":1.2},"limit":{"output":4096},"reasoning":false},"prefix/huge":{"limit":{"output":1024}},"same":{"cost":{"input":2,"output":4}}}},"deepseek":{"models":{"chat":{"cost":{"input":0.27,"output":1.1,"cache_read":0.07},"limit":{"context":128000,"output":8192},"modalities":{"input":["text"]},"tool_call":true,"reasoning":true,"release_date":"2026-02-03"},"huge":{"limit":{"output":1000001}}}},"anthropic":{"models":{"claude":{"cost":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75},"limit":{"context":200000,"output":32000},"modalities":{"input":[]},"tool_call":true},"incomplete":{"cost":{"input":"x","output":1},"limit":{"context":8000},"tool_call":false}}},"tie-first":{"models":{"same":{"cost":{"input":1,"output":3}}}},"free":{"models":{"empty":{}}}}`
	data, err := parseCatalog(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	type ask struct {
		Model string `json:"model"`
		Hint  string `json:"hint"`
	}
	asks := []ask{{" CHAT ", ""}, {"chat", "https://openrouter.ai/api"}, {"chat", "reseller"}, {"claude", ""}, {"bare", ""}, {"empty", ""}, {"unknown", ""}, {"incomplete", ""}, {"huge", ""}, {"same", ""}, {"same", "tie-first"}, {"deepseek/chat", ""}}
	output := []any{}
	for _, ask := range asks {
		var match *PriceMatch
		for _, entry := range data.matches(ask.Model, ask.Hint) {
			if entry.info.Price != nil {
				match = &PriceMatch{Source: entry.provider, Price: *copyInfo(entry.info).Price}
				break
			}
		}
		output = append(output, map[string]any{"lookup": match, "info": describe(data, []string{ask.Model}, ask.Hint)})
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("ARIADNE_REQUIRE_LEGACY") == "1" {
			t.Fatal(err)
		}
		return
	}
	input, _ := json.Marshal(map[string]any{"catalog": json.RawMessage(raw), "asks": asks})
	command := legacytest.Command(t, bun, "packages/server/src/testing/go-model-catalog-fixture.ts")
	command.Dir = filepath.Join("..", "..")
	command.Stdin = strings.NewReader(string(input))
	want, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("legacy %v\n%s", err, want)
	}
	got, _ := json.Marshal(output)
	var actual, expected any
	if json.Unmarshal(got, &actual) != nil || json.Unmarshal(want, &expected) != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Go %s\nTS %s", got, want)
	}
}

func TestModelCatalogSingleflightExpiryAndOwnership(t *testing.T) {
	var calls atomic.Int32
	var now atomic.Int64
	now.Store(time.Now().UnixMilli())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Accept") != "application/json" {
			t.Error("wrong Accept header")
		}
		io.WriteString(w, `{"openai":{"models":{"m":{"cost":{"input":1,"output":2},"limit":{"context":32000},"modalities":{"input":["image"]}}}}}`)
	}))
	defer server.Close()
	catalog := newTestCatalog(t, Options{URL: server.URL, Now: func() time.Time { return time.UnixMilli(now.Load()) }, TTL: time.Minute, Reap: time.Millisecond})
	var group sync.WaitGroup
	for range 30 {
		group.Add(1)
		go func() {
			defer group.Done()
			info, err := catalog.Describe(t.Context(), []string{"m"}, "")
			if err != nil || info["m"].Price == nil {
				t.Error(info, err)
			}
		}()
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate catalog downloads", calls.Load())
	}
	info, err := catalog.Describe(t.Context(), []string{"m"}, "")
	if err != nil {
		t.Fatal(err)
	}
	*info["m"].ContextTokens = 1
	info["m"].Price.Input = 99
	*info["m"].Images = false
	info, err = catalog.Describe(t.Context(), []string{"m"}, "")
	if err != nil || *info["m"].ContextTokens != 32000 || info["m"].Price.Input != 1 || !*info["m"].Images {
		t.Fatal("caller mutated cached catalog", info, err)
	}
	now.Add(int64(time.Hour / time.Millisecond))
	deadline := time.Now().Add(time.Second)
	for {
		catalog.mu.Lock()
		expired := catalog.cache == nil
		catalog.mu.Unlock()
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unused catalog retained after expiry")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := catalog.Describe(t.Context(), []string{"m"}, ""); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("expired catalog not refreshed")
	}
	if err := catalog.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	catalog.mu.Lock()
	retained := catalog.cache != nil || catalog.loading != nil
	catalog.mu.Unlock()
	if retained {
		t.Fatal("closed catalog retained data")
	}
}
func TestCatalogCancelledWaiterAndClose(t *testing.T) {
	started, release, ended := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		defer close(ended)
		select {
		case <-release:
			io.WriteString(w, `{"openai":{"models":{"m":{}}}}`)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	catalog := newTestCatalog(t, Options{URL: server.URL})
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, err := catalog.Describe(ctx, []string{"m"}, ""); first <- err }()
	<-started
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	second := make(chan error, 1)
	go func() { _, err := catalog.Describe(t.Context(), []string{"m"}, ""); second <- err }()
	close(release)
	if err := <-second; err != nil {
		t.Fatal("one waiter cancelled shared fetch", err)
	}
	<-ended
	if err := catalog.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestCatalogCloseCancelsFetchAndRejectsMalformed(t *testing.T) {
	started, ended := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); defer close(ended); <-r.Context().Done() }))
	defer server.Close()
	catalog := newTestCatalog(t, Options{URL: server.URL})
	done := make(chan error, 1)
	go func() { _, err := catalog.Describe(t.Context(), []string{"m"}, ""); done <- err }()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := catalog.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal("fetch retained after close")
	}
	if err := <-done; err == nil {
		t.Fatal("closed catalog request succeeded")
	}
	for _, raw := range []string{`null`, `[]`, `{`, `{} {}`, `{"x":{"models":{"m":`} {
		if _, err := parseCatalog(strings.NewReader(raw)); err == nil {
			t.Fatal("invalid catalog accepted", raw)
		}
	}
}

func TestCatalogResourceBoundsAndOrdering(t *testing.T) {
	raw := `{"openai":{"models":{"one":{},"two":{},"three":{}}}}`
	for _, limits := range []parseLimits{{body: 10, models: 10, index: 10000}, {body: 10000, models: 2, index: 10000}, {body: 10000, models: 10, index: 400}} {
		if _, err := parseBounded(strings.NewReader(raw), limits); err == nil {
			t.Fatal("missing catalog limit", limits)
		}
	}
	known := map[string]Info{"old": {Released: "2025-01-01"}, "new": {Released: "2026-01-01"}, "embedding": {Released: "2026-11-01", Tools: new(false)}}
	if order := InOrder([]string{"unknown", "embedding", "old", "new"}, known); !reflect.DeepEqual(order, []string{"new", "old", "unknown", "embedding"}) {
		t.Fatal(order)
	}
	if name := SuggestModel([]string{"embedding", "old", "new", "mini"}, known); name != "new" {
		t.Fatal(name)
	}
}
