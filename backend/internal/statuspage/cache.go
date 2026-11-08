package statuspage

import (
	"sync"
	"time"
)

// pageCacheBound is the maximum number of cached slugs. The bound keeps random
// slug requests from growing the cache without limit even though misses are
// never cached.
const pageCacheBound = 1024

// pageCacheEntry is a cached payload with its insertion time and a monotonic
// sequence used to break eviction ties deterministically.
type pageCacheEntry struct {
	page       PublicPage
	insertedAt time.Time
	sequence   uint64
}

// pageCache is a mutex-guarded, bounded TTL cache of public status payloads.
type pageCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	nextSeq uint64
	entries map[string]pageCacheEntry
}

// newPageCache returns a cache with the given TTL that reads the current time
// through now. A nil now defaults to time.Now.
func newPageCache(ttl time.Duration, now func() time.Time) *pageCache {
	if now == nil {
		now = time.Now
	}
	return &pageCache{
		ttl:     ttl,
		now:     now,
		entries: make(map[string]pageCacheEntry),
	}
}

// get returns the cached page for slug. Expired entries are removed and
// reported as misses. Returned pages are read-only and share slices with the
// cached entry.
func (c *pageCache) get(slug string) (PublicPage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[slug]
	if !ok {
		return PublicPage{}, false
	}
	if c.now().Sub(entry.insertedAt) >= c.ttl {
		delete(c.entries, slug)
		return PublicPage{}, false
	}
	return entry.page, true
}

// set stores page under slug, overwriting any existing entry, and trims the
// cache when it grows past its bound.
func (c *pageCache) set(slug string, page PublicPage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextSeq++
	c.entries[slug] = pageCacheEntry{
		page:       page,
		insertedAt: c.now(),
		sequence:   c.nextSeq,
	}
	if len(c.entries) > pageCacheBound {
		c.evict()
	}
}

// delete removes slug from the cache.
func (c *pageCache) delete(slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, slug)
}

// len reports the number of cached entries. It is used only by tests.
func (c *pageCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.entries)
}

// evict removes expired entries first, then the oldest insertions until the
// cache is back within its bound. The caller must hold c.mu.
func (c *pageCache) evict() {
	now := c.now()
	for slug, entry := range c.entries {
		if now.Sub(entry.insertedAt) >= c.ttl {
			delete(c.entries, slug)
		}
	}
	for len(c.entries) > pageCacheBound {
		var (
			oldestSlug string
			oldestSeq  uint64
			found      bool
		)
		for slug, entry := range c.entries {
			if !found || entry.sequence < oldestSeq {
				oldestSlug = slug
				oldestSeq = entry.sequence
				found = true
			}
		}
		delete(c.entries, oldestSlug)
	}
}
