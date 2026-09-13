package audit

import (
	"context"
	"sync"
)

type gateEntry struct {
	semaphore chan struct{}
	refs      int
}
type keyedGate struct {
	mu      sync.Mutex
	entries map[string]*gateEntry
}

// Waiting requests retain their own cancellation and execute again after the
// leader finishes, so a cancelled leader cannot cancel unrelated callers.
func (g *keyedGate) acquire(ctx context.Context, key string) (func(), error) {
	g.mu.Lock()
	if g.entries == nil {
		g.entries = make(map[string]*gateEntry)
	}
	e := g.entries[key]
	if e == nil {
		e = &gateEntry{semaphore: make(chan struct{}, 1)}
		g.entries[key] = e
	}
	e.refs++
	g.mu.Unlock()
	drop := func() {
		g.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(g.entries, key)
		}
		g.mu.Unlock()
	}
	select {
	case e.semaphore <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-e.semaphore; drop() }) }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}
