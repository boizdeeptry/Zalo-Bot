package daemon

import (
	"context"
	"errors"
	"sync"
)

var errAppZaloThreadIDRequired = errors.New("Zalo thread ID is required")

type appZaloThreadGate struct {
	mu      sync.Mutex
	entries map[string]*appZaloThreadGateEntry
}

type appZaloThreadGateEntry struct {
	token chan struct{}
	refs  int
}

func (g *appZaloThreadGate) Acquire(ctx context.Context, threadID string) (func(), error) {
	if threadID == "" {
		return nil, errAppZaloThreadIDRequired
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entry := g.reference(threadID)
	select {
	case <-ctx.Done():
		g.unreference(threadID, entry)
		return nil, ctx.Err()
	case <-entry.token:
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			entry.token <- struct{}{}
			g.unreference(threadID, entry)
		})
	}, nil
}

func (g *appZaloThreadGate) Len() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.entries)
}

func (g *appZaloThreadGate) reference(threadID string) *appZaloThreadGateEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.entries == nil {
		g.entries = make(map[string]*appZaloThreadGateEntry)
	}
	entry := g.entries[threadID]
	if entry == nil {
		entry = &appZaloThreadGateEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		g.entries[threadID] = entry
	}
	entry.refs++
	return entry
}

func (g *appZaloThreadGate) unreference(threadID string, entry *appZaloThreadGateEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.entries[threadID] != entry {
		return
	}
	entry.refs--
	if entry.refs == 0 {
		delete(g.entries, threadID)
	}
}
