package storage

import (
	"container/list"
	"sync"
)

// entry is an item stored in the LRU cache.
type entry struct {
	key  string
	val  []byte
	size int
}

// LRUCache is a simple in-memory LRU cache with a byte-size cap.
// It is safe for concurrent use.
type LRUCache struct {
	mu      sync.Mutex
	cap     int // maximum total byte size
	used    int // current total byte size
	ll      *list.List
	items   map[string]*list.Element
}

// NewLRUCache creates a new LRUCache with the given byte capacity.
func NewLRUCache(cap int) *LRUCache {
	return &LRUCache{
		cap:   cap,
		ll:    list.New(),
		items: make(map[string]*list.Element),
	}
}

// Get returns the cached value for key, and whether it was found.
func (c *LRUCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*entry).val, true
}

// Set stores val under key, evicting the least-recently-used entries as needed
// to stay within the byte capacity. Values larger than the total capacity are
// not stored.
func (c *LRUCache) Set(key string, val []byte) {
	size := len(val)
	if size > c.cap {
		return // single item exceeds total cap; skip
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Update existing entry.
	if el, ok := c.items[key]; ok {
		e := el.Value.(*entry)
		c.used -= e.size
		c.used += size
		e.val = val
		e.size = size
		c.ll.MoveToFront(el)
		c.evict()
		return
	}

	// Insert new entry.
	e := &entry{key: key, val: val, size: size}
	el := c.ll.PushFront(e)
	c.items[key] = el
	c.used += size
	c.evict()
}

// Delete removes key from the cache if present.
func (c *LRUCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		c.removeElement(el)
	}
}

// evict removes the least-recently-used entries until used <= cap.
// Must be called with c.mu held.
func (c *LRUCache) evict() {
	for c.used > c.cap {
		el := c.ll.Back()
		if el == nil {
			break
		}
		c.removeElement(el)
	}
}

// removeElement removes an element from the list and map.
// Must be called with c.mu held.
func (c *LRUCache) removeElement(el *list.Element) {
	e := el.Value.(*entry)
	c.ll.Remove(el)
	delete(c.items, e.key)
	c.used -= e.size
}
