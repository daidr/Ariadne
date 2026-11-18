package links

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

func fixture() sdk.LinkProvider {
	return sdk.LinkProvider{Kind: "fixture", Match: func(address *url.URL) *sdk.LinkMatch {
		if address.Host == "example.test" {
			return &sdk.LinkMatch{Kind: "pull-request", Label: "acme/app#1"}
		}
		return nil
	}, Preview: func(context.Context, *url.URL) (*sdk.PullRequestPreview, error) {
		author := "alice"
		return &sdk.PullRequestPreview{Kind: "pull-request", Title: "preview", Author: &author}, nil
	}}
}
func TestSummariesRemainOfflineAndSkipBadLinksOrProviders(t *testing.T) {
	resolver := New(Options{})
	defer resolver.Close()
	broken := fixture()
	broken.Kind = "broken"
	broken.Match = func(*url.URL) *sdk.LinkMatch { panic("plugin error") }
	resolver.Register(broken)
	value := fixture()
	value.Preview = func(context.Context, *url.URL) (*sdk.PullRequestPreview, error) {
		t.Error("summary contacted remote")
		return nil, nil
	}
	resolver.Register(value)
	addresses := []string{"https://EXAMPLE.test:443", "http://example.test", "https://other.test/", strings.Repeat("x", 2001), "not a URL"}
	summaries := resolver.Summarize(addresses)
	if summaries[addresses[0]] == nil || summaries[addresses[0]].Label != "acme/app#1" {
		t.Fatal("normalized link missing")
	}
	for _, address := range addresses[1:] {
		if summaries[address] != nil {
			t.Fatal("bad link recognized", address)
		}
	}
	for i := 0; i < 250; i++ {
		addresses = append(addresses, fmt.Sprintf("https://example.test/%d", i))
	}
	if len(resolver.Summarize(addresses)) != MaxLinks {
		t.Fatal("summary batch unbounded")
	}
}
func TestPreviewSharesLookupCopiesResultsAndExpiresWithoutAccess(t *testing.T) {
	resolver := New(Options{TTL: 100 * time.Millisecond})
	defer resolver.Close()
	value := fixture()
	var calls atomic.Int32
	started := make(chan struct{})
	ready := make(chan struct{})
	value.Preview = func(ctx context.Context, address *url.URL) (*sdk.PullRequestPreview, error) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-ready:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return fixture().Preview(ctx, address)
	}
	resolver.Register(value)
	ctx, cancel := context.WithCancel(t.Context())
	cancelled := make(chan *sdk.PullRequestPreview, 1)
	go func() { cancelled <- resolver.Preview(ctx, "https://example.test/") }()
	<-started
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if preview := resolver.Preview(t.Context(), "https://example.test/"); preview == nil || preview.Title != "preview" {
				failures.Add(1)
			}
		}()
	}
	cancel()
	if <-cancelled != nil {
		t.Fatal("cancelled waiter received value")
	}
	close(ready)
	wg.Wait()
	if calls.Load() != 1 || failures.Load() != 0 {
		t.Fatal("lookup not shared", calls.Load(), failures.Load())
	}
	first := resolver.Preview(t.Context(), "https://example.test/")
	first.Title = "mutated"
	*first.Author = "changed"
	second := resolver.Preview(t.Context(), "https://example.test/")
	if second.Title != "preview" || *second.Author != "alice" {
		t.Fatal("caller mutated shared preview")
	}
	deadline := time.Now().Add(time.Second)
	for {
		entries, _ := resolver.cache.Stats()
		if entries == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired preview retained")
		}
		time.Sleep(time.Millisecond)
	}
	if resolver.Preview(t.Context(), "https://example.test/") == nil || calls.Load() != 2 {
		t.Fatal("expired preview not refreshed")
	}
}
func TestFailuresRetryAndProviderRemovalDropsCacheAndPending(t *testing.T) {
	resolver := New(Options{})
	defer resolver.Close()
	value := fixture()
	var calls atomic.Int32
	value.Preview = func(ctx context.Context, address *url.URL) (*sdk.PullRequestPreview, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("retry")
		}
		return fixture().Preview(ctx, address)
	}
	remove, err := resolver.Register(value)
	if err != nil {
		t.Fatal(err)
	}
	if resolver.Preview(t.Context(), "https://example.test/") != nil {
		t.Fatal("failure became preview")
	}
	if resolver.Preview(t.Context(), "https://example.test/") == nil || calls.Load() != 2 {
		t.Fatal("failure cached")
	}
	remove()
	if entries, _ := resolver.cache.Stats(); entries != 0 {
		t.Fatal("unregistered provider retained previews")
	}
	started := make(chan struct{})
	finished := make(chan struct{})
	value.Preview = func(ctx context.Context, _ *url.URL) (*sdk.PullRequestPreview, error) {
		close(started)
		defer close(finished)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	remove, err = resolver.Register(value)
	if err != nil {
		t.Fatal(err)
	}
	request := make(chan *sdk.PullRequestPreview, 1)
	go func() { request <- resolver.Preview(t.Context(), "https://example.test/") }()
	<-started
	remove()
	select {
	case <-finished:
	default:
		t.Fatal("unregister returned before request was released")
	}
	if <-request != nil {
		t.Fatal("removed provider returned preview")
	}
	replacement := fixture()
	replacement.Preview = func(context.Context, *url.URL) (*sdk.PullRequestPreview, error) {
		return &sdk.PullRequestPreview{Kind: "pull-request", Title: "replacement"}, nil
	}
	resolver.Register(replacement)
	if got := resolver.Preview(t.Context(), "https://example.test/"); got == nil || got.Title != "replacement" {
		t.Fatal("old provider cache reused")
	}
}
func TestCacheBoundsCapacityTimeoutAndClose(t *testing.T) {
	resolver := New(Options{Timeout: time.Second})
	resolver.Register(fixture())
	for i := 0; i < 510; i++ {
		if resolver.Preview(t.Context(), fmt.Sprintf("https://example.test/%d", i)) == nil {
			t.Fatal("preview missing")
		}
	}
	if entries, weight := resolver.cache.Stats(); entries != 500 || weight > 4*1024*1024 {
		t.Fatal("cache unbounded", entries, weight)
	}
	resolver.Close()
	if entries, weight := resolver.cache.Stats(); entries != 0 || weight != 0 {
		t.Fatal("close retained cache")
	}
	resolver = New(Options{Timeout: 30 * time.Millisecond})
	defer resolver.Close()
	value := fixture()
	value.Preview = func(ctx context.Context, _ *url.URL) (*sdk.PullRequestPreview, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	resolver.Register(value)
	started := time.Now()
	if resolver.Preview(t.Context(), "https://example.test/") != nil || time.Since(started) > time.Second {
		t.Fatal("preview timeout failed")
	}
	resolver.Close()
	resolver.mu.Lock()
	pending := len(resolver.pending)
	providers := len(resolver.providers)
	resolver.mu.Unlock()
	if pending != 0 || providers != 0 {
		t.Fatal("close retained work")
	}
}
