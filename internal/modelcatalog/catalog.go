// Package modelcatalog reads bounded public model metadata. The single cached
// catalog expires independently of subsequent requests and never contains keys.
package modelcatalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/daidr/Ariadne/sdk"
)

const URL = "https://models.dev/api.json"

type Options struct {
	Client    *http.Client
	URL       string
	Context   context.Context
	Now       func() time.Time
	TTL, Reap time.Duration
}
type flight struct {
	done chan struct{}
	data *snapshot
	err  error
}
type Catalog struct {
	mu      sync.Mutex
	options Options
	ctx     context.Context
	cancel  context.CancelFunc
	cache   *snapshot
	at      time.Time
	loading *flight
	done    chan struct{}
	closed  bool
}
type PriceMatch struct {
	Price  sdk.ModelPrice `json:"price"`
	Source string         `json:"source"`
}

func New(options Options) *Catalog {
	if options.Context == nil {
		options.Context = context.Background()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.URL == "" {
		options.URL = URL
	}
	if options.TTL <= 0 {
		options.TTL = 12 * time.Hour
	}
	if options.Reap <= 0 {
		options.Reap = min(time.Minute, options.TTL)
	}
	ctx, cancel := context.WithCancel(options.Context)
	catalog := &Catalog{options: options, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go catalog.reap()
	return catalog
}
func (catalog *Catalog) reap() {
	defer close(catalog.done)
	ticker := time.NewTicker(catalog.options.Reap)
	defer ticker.Stop()
	for {
		select {
		case <-catalog.ctx.Done():
			catalog.mu.Lock()
			catalog.cache = nil
			catalog.mu.Unlock()
			return
		case <-ticker.C:
			catalog.mu.Lock()
			if catalog.cache != nil && catalog.options.Now().Sub(catalog.at) >= catalog.options.TTL {
				catalog.cache = nil
			}
			catalog.mu.Unlock()
		}
	}
}
func (catalog *Catalog) fetch(ctx context.Context) (*snapshot, error) {
	request, err := http.NewRequestWithContext(ctx, "GET", catalog.options.URL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	client := catalog.options.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("the price catalog answered %d", response.StatusCode)
	}
	return parseCatalog(response.Body)
}
func (catalog *Catalog) load(ctx context.Context) (*snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	catalog.mu.Lock()
	if catalog.closed || catalog.ctx.Err() != nil {
		catalog.mu.Unlock()
		return nil, errors.New("model catalog is closed")
	}
	if catalog.cache != nil && catalog.options.Now().Sub(catalog.at) < catalog.options.TTL {
		data := catalog.cache
		catalog.mu.Unlock()
		return data, nil
	}
	catalog.cache = nil
	loading := catalog.loading
	if loading == nil {
		loading = &flight{done: make(chan struct{})}
		catalog.loading = loading
		go func() {
			fetchCtx, cancel := context.WithTimeout(catalog.ctx, 15*time.Second)
			defer cancel()
			data, err := catalog.fetch(fetchCtx)
			catalog.mu.Lock()
			defer catalog.mu.Unlock()
			loading.data, loading.err = data, err
			if err == nil && !catalog.closed && catalog.ctx.Err() == nil {
				catalog.cache = data
				catalog.at = catalog.options.Now()
			}
			catalog.loading = nil
			close(loading.done)
		}()
	}
	catalog.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-catalog.ctx.Done():
		return nil, errors.New("model catalog is closed")
	case <-loading.done:
		return loading.data, loading.err
	}
}
func (catalog *Catalog) Describe(ctx context.Context, models []string, hint string) (map[string]Info, error) {
	data, err := catalog.load(ctx)
	if err != nil {
		return nil, err
	}
	return describe(data, models, hint), nil
}
func (catalog *Catalog) Lookup(ctx context.Context, model, hint string) (*PriceMatch, error) {
	data, err := catalog.load(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range data.matches(model, hint) {
		if entry.info.Price != nil {
			return &PriceMatch{Source: entry.provider, Price: *copyInfo(entry.info).Price}, nil
		}
	}
	return nil, nil
}
func (catalog *Catalog) Close(ctx context.Context) error {
	catalog.mu.Lock()
	catalog.closed = true
	catalog.cache = nil
	catalog.cancel()
	loading := catalog.loading
	catalog.mu.Unlock()
	select {
	case <-catalog.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if loading != nil {
		select {
		case <-loading.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
