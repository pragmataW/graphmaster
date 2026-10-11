package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

// defaultNameCacheSize bounds the in-process intern cache (entries).
const defaultNameCacheSize = 1 << 20

// nameCache maps interned names to ids for one database file. Only committed
// ids are stored: names table rows are never deleted, so a committed id
// stays valid for the life of the file.
type nameCache struct {
	mu    sync.RWMutex
	m     map[string]int64
	limit int
}

func newNameCache(limit int) *nameCache {
	return &nameCache{m: make(map[string]int64), limit: limit}
}

func (c *nameCache) get(name string) (int64, bool) {
	c.mu.RLock()
	id, ok := c.m[name]
	c.mu.RUnlock()
	return id, ok
}

func (c *nameCache) put(name string, id int64) {
	c.mu.Lock()
	c.putLocked(name, id)
	c.mu.Unlock()
}

func (c *nameCache) putAll(m map[string]int64) {
	if len(m) == 0 {
		return
	}
	c.mu.Lock()
	for n, id := range m {
		c.putLocked(n, id)
	}
	c.mu.Unlock()
}

// putLocked inserts one entry; when the cache is full it is simply cleared,
// which is cheap and keeps memory bounded on huge repositories.
func (c *nameCache) putLocked(name string, id int64) {
	if _, ok := c.m[name]; !ok && len(c.m) >= c.limit {
		c.m = make(map[string]int64)
	}
	c.m[name] = id
}

func (c *nameCache) size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}

func (c *nameCache) reset() {
	c.mu.Lock()
	c.m = make(map[string]int64)
	c.mu.Unlock()
}

// NameID returns the id of an interned name. ok is false if the name was
// never interned.
func (s *Store) NameID(ctx context.Context, name string) (id int64, ok bool, err error) {
	if id, ok := s.names.get(name); ok {
		return id, true, nil
	}
	err = s.db.QueryRowContext(ctx, `SELECT id FROM names WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	s.names.put(name, id)
	return id, true, nil
}

// Name returns the name for an id, or ErrNotFound.
func (s *Store) Name(ctx context.Context, id int64) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM names WHERE id = ?`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}
