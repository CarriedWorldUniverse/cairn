// Package herald is cairn's consumer-side view of the herald identity
// authority for the SSH path: it maps a casket-key fingerprint to a herald
// agent (active state + scopes). The SSH ingress depends only on the
// HeraldAgents interface; the real implementation calls NEX-412
// (GET /api/agents/by-fingerprint/{fp}); a fake backs the tests; a cache
// wraps either to spare a herald round-trip per SSH connection.
package herald

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrAgentNotFound means no active herald agent owns the given fingerprint.
var ErrAgentNotFound = errors.New("herald: agent not found for fingerprint")

// Agent is the resolved herald identity behind a casket key.
type Agent struct {
	ID          string   // herald agent id (the actor recorded on pushes)
	OrgID       string   // owning org
	Active      bool     // herald's liveness/block cascade result
	Scopes      []string // e.g. ["repo:read","repo:write"]
	Fingerprint string   // the casket fingerprint that resolved to this agent
}

// HasScope reports whether the agent holds the named scope.
func (a Agent) HasScope(s string) bool {
	for _, have := range a.Scopes {
		if have == s {
			return true
		}
	}
	return false
}

// HeraldAgents resolves a casket fingerprint to a herald agent. The SSH
// ingress is written against this interface alone.
type HeraldAgents interface {
	LookupByFingerprint(ctx context.Context, fp string) (Agent, error)
}

// CachedAgents wraps a HeraldAgents with a short-TTL positive cache and an
// explicit Invalidate (the block-invalidation hook). Negative results are not
// cached — a just-provisioned agent must resolve immediately.
type CachedAgents struct {
	backend HeraldAgents
	ttl     time.Duration
	now     func() time.Time

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	agent   Agent
	expires time.Time
}

// NewCachedAgents wraps backend with the given positive-cache TTL.
func NewCachedAgents(backend HeraldAgents, ttl time.Duration) *CachedAgents {
	return &CachedAgents{
		backend: backend,
		ttl:     ttl,
		now:     time.Now,
		entries: map[string]cacheEntry{},
	}
}

// LookupByFingerprint serves a non-expired cached agent, else fetches and
// caches it.
func (c *CachedAgents) LookupByFingerprint(ctx context.Context, fp string) (Agent, error) {
	now := c.now()
	c.mu.Lock()
	if e, ok := c.entries[fp]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.agent, nil
	}
	c.mu.Unlock()

	a, err := c.backend.LookupByFingerprint(ctx, fp)
	if err != nil {
		return Agent{}, err
	}
	c.mu.Lock()
	c.entries[fp] = cacheEntry{agent: a, expires: now.Add(c.ttl)}
	c.mu.Unlock()
	return a, nil
}

// Invalidate drops any cached entry for a fingerprint. Call this when herald
// signals an agent was blocked (block-invalidation), so a darkened agent
// can't ride a stale cache entry until the TTL lapses.
func (c *CachedAgents) Invalidate(fp string) {
	c.mu.Lock()
	delete(c.entries, fp)
	c.mu.Unlock()
}
