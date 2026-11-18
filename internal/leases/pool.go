// Package leases shares resources by configuration. Only idle resources expire;
// an active lease keeps its resource alive until the operation releases it.
package leases

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"
)

type Resource interface{ Close() error }
type Options struct {
	Idle             int
	TTL, LoadTimeout time.Duration
	Now              func() time.Time
}
type entry[T Resource] struct {
	id, key                  string
	value                    T
	err                      error
	ready, closed            chan struct{}
	loaded, retired, closing bool
	refs, waiters            int
	used                     time.Time
	ctx                      context.Context
	cancel                   context.CancelFunc
}
type Pool[T Resource] struct {
	mu                         sync.Mutex
	entries                    map[string]*entry[T]
	all                        map[*entry[T]]bool
	options                    Options
	life                       context.Context
	cancel                     context.CancelFunc
	jobs                       sync.WaitGroup
	loading                    int
	closed                     bool
	done, stopSweep, sweepDone chan struct{}
	closeErr                   error
}
type Lease[T Resource] struct {
	Value   T
	Release func()
	ctx     context.Context
}

func (lease Lease[T]) Context(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(lease.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}
func New[T Resource](options Options) *Pool[T] {
	if options.Idle <= 0 {
		options.Idle = 16
	}
	if options.TTL <= 0 {
		options.TTL = 20 * time.Minute
	}
	if options.LoadTimeout <= 0 {
		options.LoadTimeout = 2 * time.Minute
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	life, cancel := context.WithCancel(context.Background())
	pool := &Pool[T]{entries: map[string]*entry[T]{}, all: map[*entry[T]]bool{}, options: options, life: life, cancel: cancel, done: make(chan struct{}), stopSweep: make(chan struct{}), sweepDone: make(chan struct{})}
	go func() {
		defer close(pool.sweepDone)
		ticker := time.NewTicker(min(options.TTL, time.Minute))
		defer ticker.Stop()
		for {
			select {
			case <-pool.stopSweep:
				return
			case <-ticker.C:
				pool.Purge()
			}
		}
	}()
	return pool
}
func load[T Resource](ctx context.Context, loader func(context.Context) (T, error)) (value T, err error) {
	defer func() {
		if recover() != nil {
			var zero T
			value = zero
			err = errors.New("resource loader failed")
		}
	}()
	return loader(ctx)
}
func closeResource[T Resource](value T) (err error) {
	raw := reflect.ValueOf(value)
	if !raw.IsValid() || (raw.Kind() == reflect.Pointer || raw.Kind() == reflect.Interface || raw.Kind() == reflect.Map || raw.Kind() == reflect.Slice || raw.Kind() == reflect.Func || raw.Kind() == reflect.Chan) && raw.IsNil() {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = errors.New("resource close failed")
		}
	}()
	return value.Close()
}
func (pool *Pool[T]) closeEntry(entry *entry[T]) {
	if entry.closing || !entry.loaded || entry.refs > 0 {
		return
	}
	entry.closing = true
	entry.cancel()
	pool.jobs.Add(1)
	go func() {
		defer pool.jobs.Done()
		err := closeResource(entry.value)
		pool.mu.Lock()
		var zero T
		entry.value = zero
		delete(pool.all, entry)
		close(entry.closed)
		if pool.closeErr == nil && err != nil {
			message := err.Error()
			if len(message) > 1024 {
				message = message[:1024]
			}
			pool.closeErr = errors.New(strings.Clone(message))
		}
		pool.mu.Unlock()
	}()
}
func (pool *Pool[T]) retire(entry *entry[T], interrupt bool) {
	entry.retired = true
	if pool.entries[entry.id] == entry {
		delete(pool.entries, entry.id)
	}
	if interrupt || entry.refs == 0 {
		entry.cancel()
	}
	pool.closeEntry(entry)
}
func (pool *Pool[T]) trim() {
	for {
		idle := 0
		var oldest *entry[T]
		for _, entry := range pool.entries {
			if entry.loaded && entry.refs == 0 && entry.waiters == 0 {
				idle++
				if oldest == nil || entry.used.Before(oldest.used) {
					oldest = entry
				}
			}
		}
		if idle <= pool.options.Idle {
			return
		}
		pool.retire(oldest, false)
	}
}
func (pool *Pool[T]) Acquire(ctx context.Context, id, key string, loader func(context.Context) (T, error)) (Lease[T], error) {
	var empty Lease[T]
	if id == "" || len(id) > 1024 || len(key) > 1024 || loader == nil {
		return empty, errors.New("invalid resource identity or loader")
	}
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return empty, errors.New("resource pool is closed")
	}
	if err := ctx.Err(); err != nil {
		pool.mu.Unlock()
		return empty, err
	}
	known := pool.entries[id]
	if known != nil && known.key != key {
		pool.retire(known, false)
		known = nil
	}
	if known == nil {
		if pool.loading >= 128 {
			pool.mu.Unlock()
			return empty, errors.New("too many pending resource connections")
		}
		life, cancel := context.WithCancel(pool.life)
		known = &entry[T]{id: strings.Clone(id), key: strings.Clone(key), ready: make(chan struct{}), closed: make(chan struct{}), used: pool.options.Now(), ctx: life, cancel: cancel}
		pool.entries[known.id] = known
		pool.all[known] = true
		pool.loading++
		pool.jobs.Add(1)
		go func(entry *entry[T]) {
			defer pool.jobs.Done()
			ctx, cancel := context.WithTimeout(entry.ctx, pool.options.LoadTimeout)
			defer cancel()
			value, err := load(ctx, loader)
			pool.mu.Lock()
			defer pool.mu.Unlock()
			pool.loading--
			entry.value, entry.err, entry.loaded = value, err, true
			if err != nil || ctx.Err() != nil || pool.closed || entry.retired {
				if err == nil {
					entry.err = ctx.Err()
					if entry.err == nil {
						entry.err = errors.New("resource configuration changed")
					}
				}
				pool.retire(entry, true)
			}
			close(entry.ready)
			pool.trim()
		}(known)
	}
	known.waiters++
	ready := known.ready
	pool.mu.Unlock()
	select {
	case <-ctx.Done():
		pool.mu.Lock()
		known.waiters--
		pool.trim()
		pool.mu.Unlock()
		return empty, ctx.Err()
	case <-ready:
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	known.waiters--
	if err := ctx.Err(); err != nil {
		pool.trim()
		return empty, err
	}
	if known.err != nil {
		pool.trim()
		return empty, known.err
	}
	if pool.closed || known.retired || pool.entries[id] != known {
		pool.trim()
		return empty, errors.New("resource configuration changed")
	}
	known.refs++
	known.used = pool.options.Now()
	pool.jobs.Add(1)
	var once sync.Once
	release := func() {
		once.Do(func() {
			pool.mu.Lock()
			known.refs--
			known.used = pool.options.Now()
			if known.retired {
				pool.closeEntry(known)
			}
			pool.trim()
			pool.mu.Unlock()
			pool.jobs.Done()
		})
	}
	return Lease[T]{Value: known.value, Release: release, ctx: known.ctx}, nil
}
func (pool *Pool[T]) Remove(id string) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for entry := range pool.all {
		if entry.id == id {
			pool.retire(entry, true)
		}
	}
}
func (pool *Pool[T]) RemovePrefix(prefix string) {
	if prefix == "" {
		return
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for entry := range pool.all {
		if strings.HasPrefix(entry.id, prefix) {
			pool.retire(entry, true)
		}
	}
}

// RemoveWait cancels all generations and waits for their resources to close.
// The owner must prevent a new Acquire for this identity during disk removal.
func (pool *Pool[T]) RemoveWait(ctx context.Context, id string) error {
	pool.mu.Lock()
	var pending []<-chan struct{}
	for entry := range pool.all {
		if entry.id == id {
			pending = append(pending, entry.closed)
			pool.retire(entry, true)
		}
	}
	pool.mu.Unlock()
	for _, closed := range pending {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-closed:
		}
	}
	return nil
}
func (pool *Pool[T]) Busy(id string) bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for entry := range pool.all {
		if entry.id == id && (entry.refs > 0 || entry.waiters > 0 || !entry.loaded) {
			return true
		}
	}
	return false
}
func (pool *Pool[T]) Purge() {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.closed {
		return
	}
	now := pool.options.Now()
	for _, entry := range pool.entries {
		if entry.loaded && entry.refs == 0 && entry.waiters == 0 && now.Sub(entry.used) >= pool.options.TTL {
			pool.retire(entry, false)
		}
	}
	pool.trim()
}
func (pool *Pool[T]) Close(ctx context.Context) error {
	pool.mu.Lock()
	if !pool.closed {
		pool.closed = true
		pool.cancel()
		close(pool.stopSweep)
		for entry := range pool.all {
			pool.retire(entry, true)
		}
		go func() {
			pool.jobs.Wait()
			<-pool.sweepDone
			pool.mu.Lock()
			pool.options.Now = time.Now
			pool.mu.Unlock()
			close(pool.done)
		}()
	}
	pool.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-pool.done:
		pool.mu.Lock()
		err := pool.closeErr
		pool.mu.Unlock()
		return err
	}
}
