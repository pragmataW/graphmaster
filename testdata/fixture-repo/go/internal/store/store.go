package store

import "sync"

// Store persists items by ID.
type Store interface {
	Get(id string) (Item, bool)
	Put(item Item)
}

// Memory is an in-memory Store.
type Memory struct {
	mu    sync.RWMutex
	items map[string]Item
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{items: make(map[string]Item)}
}

// Get looks up an item.
func (m *Memory) Get(id string) (Item, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	it, ok := m.items[id]
	return it, ok
}

// Put stores an item, replacing any previous value.
func (m *Memory) Put(item Item) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[item.ID] = item
}

// Audited wraps Memory and counts writes. It embeds *Memory.
type Audited struct {
	*Memory
	Writes int
}

// NewAudited returns an Audited store backed by a fresh Memory.
func NewAudited() *Audited {
	return &Audited{Memory: NewMemory()}
}

// Put records the write and delegates to the embedded Memory.
func (a *Audited) Put(item Item) {
	a.Writes++
	a.Memory.Put(item)
}
