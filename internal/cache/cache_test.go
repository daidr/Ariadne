package cache

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestBoundsExpiryRemovalAndCloseReleaseReferences(t *testing.T) {
	var now atomic.Int64
	now.Store(1000)
	cache, err := New[string, *[]byte](Options{MaxEntries: 2, MaxWeight: 10, TTL: time.Second, Now: func() time.Time { return time.UnixMilli(now.Load()) }, CleanupInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	value := make([]byte, 3)
	cache.Put("a", &value, 3)
	cache.Put("b", &value, 3)
	cache.Get("a")
	cache.Put("c", &value, 3)
	if _, found := cache.Get("b"); found {
		t.Fatal("LRU entry retained")
	}
	cache.Put("d", &value, 8)
	if entries, weight := cache.Stats(); entries != 1 || weight != 8 {
		t.Fatal(entries, weight)
	}
	if cache.Put("too large", &value, 11) {
		t.Fatal("oversized value retained")
	}
	cache.Remove("d")
	if entries, weight := cache.Stats(); entries != 0 || weight != 0 {
		t.Fatal(entries, weight)
	}
	cache.Put("expires", &value, 3)
	now.Add(1000)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		cache.mu.Lock()
		remaining := len(cache.items)
		weight := cache.weight
		cache.mu.Unlock()
		if remaining == 0 {
			if weight != 0 {
				t.Fatal(weight)
			}
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("expiry cleanup did not release values")
		case <-ticker.C:
		}
	}
	cache.Put("close", &value, 3)
	cache.Close()
	cache.Close()
	if cache.Put("after", &value, 3) {
		t.Fatal("closed cache accepted a value")
	}
	if entries, weight := cache.Stats(); entries != 0 || weight != 0 {
		t.Fatal(entries, weight)
	}
}
