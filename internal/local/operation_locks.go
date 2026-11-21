package local

import (
	"context"
	"sync"
)

// Shared clones can be connected by several projects. Only live operations keep
// a lock entry; neither a closed connection nor a canceled waiter retains one.
var hostedOperations = struct {
	sync.Mutex
	entries map[string]*threadLock
}{entries: map[string]*threadLock{}}

func lockHosted(ctx context.Context, key string) (func(), error) {
	key = worktreePathKey(key)
	hostedOperations.Lock()
	entry := hostedOperations.entries[key]
	if entry == nil {
		entry = &threadLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		hostedOperations.entries[key] = entry
	}
	entry.users++
	hostedOperations.Unlock()
	release := func() {
		hostedOperations.Lock()
		entry.users--
		if entry.users == 0 {
			delete(hostedOperations.entries, key)
			if len(hostedOperations.entries) == 0 {
				hostedOperations.entries = map[string]*threadLock{}
			}
		}
		hostedOperations.Unlock()
	}
	select {
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	case <-entry.token:
	}
	return func() { entry.token <- struct{}{}; release() }, nil
}
