package herald

import (
	"context"
	"sync"
)

// FakeAgents is an in-memory HeraldAgents for tests. It counts calls so cache
// behaviour can be asserted.
type FakeAgents struct {
	mu    sync.Mutex
	byFP  map[string]Agent
	calls int
}

// NewFakeAgents builds an empty fake.
func NewFakeAgents() *FakeAgents {
	return &FakeAgents{byFP: map[string]Agent{}}
}

// Add registers an agent under its Fingerprint.
func (f *FakeAgents) Add(a Agent) {
	f.mu.Lock()
	f.byFP[a.Fingerprint] = a
	f.mu.Unlock()
}

// LookupByFingerprint returns the registered agent or ErrAgentNotFound.
func (f *FakeAgents) LookupByFingerprint(_ context.Context, fp string) (Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	a, ok := f.byFP[fp]
	if !ok {
		return Agent{}, ErrAgentNotFound
	}
	return a, nil
}
