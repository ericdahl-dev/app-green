package resolver

import (
	"sync"

	"github.com/ericdahl-dev/app-green/internal/model"
)

// compareCache holds compare answers by (repo, base, head) for the session.
// Only Included and NotIncluded are stored: both are permanent for a SHA
// pair, and a new head is a new key. Unknown (a 404, an unexpected status)
// and errors are asked again next time.
type compareCache struct {
	mu sync.Mutex
	m  map[[3]string]model.Inclusion
}

func newCompareCache() *compareCache {
	return &compareCache{m: map[[3]string]model.Inclusion{}}
}

func (c *compareCache) get(repo, base, head string) (model.Inclusion, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	inc, ok := c.m[[3]string{repo, base, head}]
	return inc, ok
}

func (c *compareCache) put(repo, base, head string, inc model.Inclusion) {
	if inc != model.Included && inc != model.NotIncluded {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[[3]string{repo, base, head}] = inc
}
