package workspaces

import (
	"context"
	"sync"
)

type threadGate struct {
	token chan struct{}
	users int
}
type threadLocks struct {
	mu    sync.Mutex
	gates map[string]*threadGate
}

// lock only serializes opening and deleting one thread's worktree. It does not
// limit turns in other conversations, including the project's coordinator.
func (locks *threadLocks) lock(ctx context.Context, id string) (func(), error) {
	locks.mu.Lock()
	if locks.gates == nil {
		locks.gates = map[string]*threadGate{}
	}
	gate := locks.gates[id]
	if gate == nil {
		gate = &threadGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		locks.gates[id] = gate
	}
	gate.users++
	locks.mu.Unlock()
	drop := func() {
		locks.mu.Lock()
		defer locks.mu.Unlock()
		gate.users--
		if gate.users == 0 {
			delete(locks.gates, id)
		}
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-gate.token:
		var once sync.Once
		return func() { once.Do(func() { gate.token <- struct{}{}; drop() }) }, nil
	}
}
