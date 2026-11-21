// Package links names message links and shares short-lived preview reads.
package links

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/daidr/Ariadne/internal/cache"
	"github.com/daidr/Ariadne/sdk"
)

const MaxLinks = 200

type Options struct {
	Now          func() time.Time
	TTL, Timeout time.Duration
}
type provider struct {
	value sdk.LinkProvider
}
type cacheKey struct {
	address  string
	provider *provider
}
type previewValue struct{ preview *sdk.PullRequestPreview }
type pending struct {
	done   chan struct{}
	cancel context.CancelFunc
	value  *sdk.PullRequestPreview
}
type Resolver struct {
	mu        sync.Mutex
	providers map[string]*provider
	order     []string
	cache     *cache.Cache[cacheKey, previewValue]
	pending   map[cacheKey]*pending
	life      context.Context
	cancel    context.CancelFunc
	jobs      sync.WaitGroup
	options   Options
	closed    bool
	done      chan struct{}
}

func New(options Options) *Resolver {
	if options.TTL <= 0 {
		options.TTL = time.Minute
	}
	if options.Timeout <= 0 {
		options.Timeout = 10 * time.Second
	}
	values, _ := cache.New[cacheKey, previewValue](cache.Options{MaxEntries: 500, MaxWeight: 4 * 1024 * 1024, TTL: options.TTL, Now: options.Now})
	life, cancel := context.WithCancel(context.Background())
	return &Resolver{providers: map[string]*provider{}, pending: map[cacheKey]*pending{}, cache: values, life: life, cancel: cancel, options: options, done: make(chan struct{})}
}
func (resolver *Resolver) Register(value sdk.LinkProvider) (func(), error) {
	if value.Kind == "" || value.Match == nil || value.Preview == nil {
		return nil, errors.New("link provider needs kind, match and preview")
	}
	resolver.mu.Lock()
	if resolver.closed {
		resolver.mu.Unlock()
		return nil, errors.New("link resolver is closed")
	}
	if resolver.providers[value.Kind] != nil {
		resolver.mu.Unlock()
		return nil, errors.New("link provider is already registered: " + value.Kind)
	}
	entry := &provider{value: value}
	resolver.providers[value.Kind] = entry
	resolver.order = append(resolver.order, value.Kind)
	resolver.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			resolver.mu.Lock()
			if resolver.providers[value.Kind] != entry {
				resolver.mu.Unlock()
				return
			}
			delete(resolver.providers, value.Kind)
			for i, kind := range resolver.order {
				if kind == value.Kind {
					resolver.order = append(resolver.order[:i], resolver.order[i+1:]...)
					break
				}
			}
			var waiting []*pending
			for key, pending := range resolver.pending {
				if key.provider == entry {
					pending.cancel()
					waiting = append(waiting, pending)
				}
			}
			for _, key := range resolver.cache.Keys() {
				if key.provider == entry {
					resolver.cache.Remove(key)
				}
			}
			resolver.mu.Unlock()
			if value.Close != nil {
				value.Close()
			}
			for _, request := range waiting {
				<-request.done
			}
		})
	}, nil
}
func parse(address string) *url.URL {
	if len(utf16.Encode([]rune(address))) > 2000 {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" {
		return nil
	}
	parsed.Scheme = "https"
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		host += ":" + parsed.Port()
	}
	parsed.Host = host
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed
}
func safeMatch(value sdk.LinkProvider, address *url.URL) (match *sdk.LinkMatch) {
	defer func() {
		if recover() != nil {
			match = nil
		}
	}()
	return value.Match(address)
}
func safePreview(ctx context.Context, value sdk.LinkProvider, address *url.URL) (preview *sdk.PullRequestPreview, err error) {
	defer func() {
		if recover() != nil {
			preview = nil
			err = errors.New("link provider failed")
		}
	}()
	return value.Preview(ctx, address)
}
func (resolver *Resolver) find(address *url.URL) (*provider, *sdk.LinkMatch) {
	resolver.mu.Lock()
	providers := make([]*provider, 0, len(resolver.order))
	if !resolver.closed {
		for _, kind := range resolver.order {
			providers = append(providers, resolver.providers[kind])
		}
	}
	resolver.mu.Unlock()
	for _, entry := range providers {
		copy := *address
		if match := safeMatch(entry.value, &copy); match != nil {
			return entry, match
		}
	}
	return nil, nil
}
func (resolver *Resolver) Summarize(addresses []string) map[string]*sdk.LinkMatch {
	out := map[string]*sdk.LinkMatch{}
	for _, address := range addresses[:min(len(addresses), MaxLinks)] {
		var match *sdk.LinkMatch
		if parsed := parse(address); parsed != nil {
			_, match = resolver.find(parsed)
		}
		if match != nil {
			copy := *match
			match = &copy
		}
		out[address] = match
	}
	return out
}
func clone(value *sdk.PullRequestPreview) *sdk.PullRequestPreview {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Labels = slices.Clone(value.Labels)
	if value.Author != nil {
		author := *value.Author
		copy.Author = &author
	}
	if value.Additions != nil {
		n := *value.Additions
		copy.Additions = &n
	}
	if value.Deletions != nil {
		n := *value.Deletions
		copy.Deletions = &n
	}
	if value.ChangedFiles != nil {
		n := *value.ChangedFiles
		copy.ChangedFiles = &n
	}
	return &copy
}
func (resolver *Resolver) Preview(ctx context.Context, address string) *sdk.PullRequestPreview {
	parsed := parse(address)
	if parsed == nil {
		return nil
	}
	entry, match := resolver.find(parsed)
	if entry == nil {
		return nil
	}
	key := cacheKey{address: parsed.String(), provider: entry}
	resolver.mu.Lock()
	if resolver.closed || resolver.providers[entry.value.Kind] != entry || ctx.Err() != nil {
		resolver.mu.Unlock()
		return nil
	}
	if value, ok := resolver.cache.Get(key); ok {
		resolver.mu.Unlock()
		return clone(value.preview)
	}
	running := resolver.pending[key]
	if running == nil {
		if len(resolver.pending) >= 128 {
			resolver.mu.Unlock()
			return nil
		}
		readCtx, cancel := context.WithTimeout(resolver.life, resolver.options.Timeout)
		running = &pending{done: make(chan struct{}), cancel: cancel}
		resolver.pending[key] = running
		resolver.jobs.Add(1)
		go func() {
			defer resolver.jobs.Done()
			defer cancel()
			value, err := safePreview(readCtx, entry.value, parsed)
			if value != nil && value.Kind != match.Kind {
				value = nil
			}
			value = clone(value)
			resolver.mu.Lock()
			defer resolver.mu.Unlock()
			delete(resolver.pending, key)
			if err == nil && readCtx.Err() == nil && !resolver.closed && resolver.providers[entry.value.Kind] == entry {
				running.value = value
				if data, err := json.Marshal(value); err == nil {
					resolver.cache.Put(key, previewValue{value}, int64(len(data)+len(key.address)+64))
				}
			}
			close(running.done)
		}()
	}
	resolver.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil
	case <-running.done:
		return clone(running.value)
	}
}
func (resolver *Resolver) Close() error {
	resolver.mu.Lock()
	if resolver.closed {
		resolver.mu.Unlock()
		<-resolver.done
		return nil
	}
	resolver.closed = true
	resolver.cancel()
	providers := resolver.providers
	resolver.providers = map[string]*provider{}
	resolver.order = nil
	resolver.mu.Unlock()
	var closeErr error
	for _, entry := range providers {
		if entry.value.Close != nil {
			closeErr = errors.Join(closeErr, entry.value.Close())
		}
	}
	resolver.jobs.Wait()
	resolver.cache.Close()
	close(resolver.done)
	return closeErr
}
