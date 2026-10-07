package resolver

import (
	"sync"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// compareKeep is how many polls a compare answer is kept without being
// asked for.
const compareKeep = 50

// compareCache holds compare answers by (repo, base, head). Only Included
// and NotIncluded are stored: both are permanent for a SHA pair, and a new
// head is a new key. Unknown (a 404, an unexpected status) and errors are
// asked again next time. An answer nobody asked for in compareKeep polls is
// dropped (see sweep), so the cache does not grow for the whole session.
type compareCache struct {
	mu  sync.Mutex
	gen int // the current poll
	m   map[[3]string]cacheEntry
}

type cacheEntry struct {
	inc  model.Inclusion
	used int // the last poll that asked for it
}

func newCompareCache() *compareCache {
	return &compareCache{m: map[[3]string]cacheEntry{}}
}

// next starts a new poll's generation.
func (c *compareCache) next() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
}

func (c *compareCache) get(repo, base, head string) (model.Inclusion, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := [3]string{repo, base, head}
	e, ok := c.m[k]
	if ok {
		e.used = c.gen
		c.m[k] = e
	}
	return e.inc, ok
}

func (c *compareCache) put(repo, base, head string, inc model.Inclusion) {
	if inc != model.Included && inc != model.NotIncluded {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[[3]string{repo, base, head}] = cacheEntry{inc: inc, used: c.gen}
}

// sweep drops the answers not asked for in the last compareKeep polls.
func (c *compareCache) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if c.gen-e.used >= compareKeep {
			delete(c.m, k)
		}
	}
}
