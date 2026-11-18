// Package cache provides bounded, expiring metadata caches. Active operations
// and durable application data must not be placed here.
package cache

import (
	"container/list"
	"errors"
	"sync"
	"time"
)

type Options struct {
	MaxEntries      int
	MaxWeight       int64
	TTL             time.Duration
	Now             func() time.Time
	CleanupInterval time.Duration
}
type entry[K comparable, V any] struct {
	key     K
	value   V
	weight  int64
	expires time.Time
}
type Cache[K comparable, V any] struct {
	mu         sync.Mutex
	items      map[K]*list.Element
	order      *list.List
	weight     int64
	options    Options
	stop, done chan struct{}
	closed     bool
	once       sync.Once
}

func New[K comparable, V any](options Options) (*Cache[K, V], error) {
	if options.MaxEntries < 1 || options.MaxWeight < 1 || options.TTL <= 0 {
		return nil, errors.New("cache entry limit, weight limit, and TTL must be positive")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.CleanupInterval <= 0 {
		options.CleanupInterval = min(options.TTL/2, time.Minute)
		if options.CleanupInterval <= 0 {
			options.CleanupInterval = time.Nanosecond
		}
	}
	cache := &Cache[K, V]{items: map[K]*list.Element{}, order: list.New(), options: options, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(cache.done)
		ticker := time.NewTicker(options.CleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-cache.stop:
				return
			case <-ticker.C:
				cache.PurgeExpired()
			}
		}
	}()
	return cache, nil
}
func (cache *Cache[K, V]) remove(element *list.Element) {
	item := element.Value.(*entry[K, V])
	delete(cache.items, item.key)
	cache.weight -= item.weight
	cache.order.Remove(element)
	// Drop references even if an internal list element is still on the stack.
	element.Value = nil
}
func (cache *Cache[K, V]) purgeExpired() {
	now := cache.options.Now()
	for element := cache.order.Back(); element != nil; {
		previous := element.Prev()
		if !now.Before(element.Value.(*entry[K, V]).expires) {
			cache.remove(element)
		}
		element = previous
	}
}
func (cache *Cache[K, V]) PurgeExpired() {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.purgeExpired()
}
func (cache *Cache[K, V]) Get(key K) (V, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	var zero V
	if cache.closed {
		return zero, false
	}
	element := cache.items[key]
	if element == nil {
		return zero, false
	}
	item := element.Value.(*entry[K, V])
	if !cache.options.Now().Before(item.expires) {
		cache.remove(element)
		return zero, false
	}
	cache.order.MoveToFront(element)
	return item.value, true
}
func (cache *Cache[K, V]) Put(key K, value V, weight int64) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.closed {
		return false
	}
	if existing := cache.items[key]; existing != nil {
		cache.remove(existing)
	}
	if weight < 1 || weight > cache.options.MaxWeight {
		return false
	}
	cache.purgeExpired()
	for len(cache.items) >= cache.options.MaxEntries || cache.weight > cache.options.MaxWeight-weight {
		cache.remove(cache.order.Back())
	}
	item := &entry[K, V]{key: key, value: value, weight: weight, expires: cache.options.Now().Add(cache.options.TTL)}
	cache.items[key] = cache.order.PushFront(item)
	cache.weight += weight
	return true
}
func (cache *Cache[K, V]) Remove(key K) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element := cache.items[key]; element != nil {
		cache.remove(element)
	}
}
func (cache *Cache[K, V]) Stats() (int, int64) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.purgeExpired()
	return len(cache.items), cache.weight
}

func (cache *Cache[K, V]) Keys() []K {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.purgeExpired()
	keys := make([]K, 0, len(cache.items))
	for key := range cache.items {
		keys = append(keys, key)
	}
	return keys
}
func (cache *Cache[K, V]) Close() {
	cache.once.Do(func() {
		cache.mu.Lock()
		cache.closed = true
		clear(cache.items)
		cache.order.Init()
		cache.weight = 0
		cache.mu.Unlock()
		close(cache.stop)
		<-cache.done
	})
}
