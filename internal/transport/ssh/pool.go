package ssh

import (
	"context"
	"errors"
	"sync"
	"time"
)

// errPoolClosed reports use of a pool after Close.
var errPoolClosed = errors.New("ssh connection pool is closed")

// Default pool bounds. They are intentionally small: reuse helps repeated short
// operations, while idle connections and total resources stay bounded.
const (
	DefaultMaxConnections = 64
	DefaultMaxIdlePerHost = 4
	DefaultIdleLifetime   = 30 * time.Second
)

// connKey identifies connections that may be reused. Any difference in host,
// authentication material, agent, host-key alias, or trust file is a different
// boundary and is never shared.
type connKey struct {
	address      string
	port         int
	user         string
	identity     string
	identities   string
	agent        string
	hostKeyAlias string
	knownHosts   string
}

// resource is a pooled connection. alive reports whether the remote end is
// still responsive; close releases it.
type resource interface {
	alive() bool
	close() error
}

// resourceLifecycle holds the per-dial auxiliary resources (context stop and
// agent socket cleanup) that must live as long as the pooled connection.
type resourceLifecycle struct {
	stop    func() bool
	cleanup func()
}

// lifecycleAware is implemented by resources carrying per-dial cleanup.
type lifecycleAware interface {
	setLifecycle(resourceLifecycle)
}

type idleResource struct {
	res   resource
	since time.Time
}

// resourcePool reuses live connections within a connKey boundary. It bounds
// total open connections (blocking new dials when full, evicting idle ones
// first) and per-key idle connections, and expires idle connections.
type resourcePool struct {
	mu      sync.Mutex
	idle    map[connKey][]idleResource
	slots   chan struct{}
	maxIdle int
	ttl     time.Duration
	now     func() time.Time
	closed  bool
}

// trackResources attaches per-dial cleanup to a pooled resource so it runs
// exactly once when the connection is finally closed.
func (p *resourcePool) trackResources(res resource, stop func() bool, cleanup func()) {
	if aware, ok := res.(lifecycleAware); ok {
		aware.setLifecycle(resourceLifecycle{stop: stop, cleanup: cleanup})
	}
}

func newResourcePool(maxConnections, maxIdle int, ttl time.Duration, now func() time.Time) *resourcePool {
	if maxConnections < 1 {
		maxConnections = 1
	}
	if maxIdle < 1 {
		maxIdle = 1
	}
	if now == nil {
		now = time.Now
	}
	return &resourcePool{idle: make(map[connKey][]idleResource), slots: make(chan struct{}, maxConnections), maxIdle: maxIdle, ttl: ttl, now: now}
}

// reserve acquires one connection slot, evicting an idle connection or waiting.
func (p *resourcePool) reserve(ctx context.Context) error {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return errPoolClosed
	}
	select {
	case p.slots <- struct{}{}:
		return nil
	default:
	}
	p.evictOldestIdle()
	select {
	case p.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// get returns a live, unexpired idle resource for the key, if any.
func (p *resourcePool) get(key connKey) (resource, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	list := p.idle[key]
	for len(list) > 0 {
		last := list[len(list)-1]
		list = list[:len(list)-1]
		if p.now().Sub(last.since) > p.ttl || !last.res.alive() {
			_ = last.res.close()
			p.releaseSlot()
			continue
		}
		p.idle[key] = list
		return last.res, true
	}
	p.idle[key] = list
	return nil, false
}

// put returns a connection to the idle set, or closes it when the per-key idle
// bound is reached or the pool is shutting down.
func (p *resourcePool) put(key connKey, res resource) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.closeReleasingSlot(res)
		return
	}
	list := p.idle[key]
	if len(list) >= p.maxIdle {
		p.mu.Unlock()
		p.closeReleasingSlot(res)
		return
	}
	p.idle[key] = append(list, idleResource{res: res, since: p.now()})
	p.mu.Unlock()
}

// discard closes a connection and frees its slot without pooling it.
func (p *resourcePool) discard(res resource) {
	p.closeReleasingSlot(res)
}

func (p *resourcePool) closeReleasingSlot(res resource) {
	_ = res.close()
	p.releaseSlot()
}

func (p *resourcePool) releaseSlot() {
	select {
	case <-p.slots:
	default:
	}
}

func (p *resourcePool) evictOldestIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	var oldestKey connKey
	oldestIndex := -1
	var oldest time.Time
	for key, list := range p.idle {
		for i, item := range list {
			if oldestIndex == -1 || item.since.Before(oldest) {
				oldestKey, oldestIndex, oldest = key, i, item.since
			}
		}
	}
	if oldestIndex < 0 {
		return
	}
	list := p.idle[oldestKey]
	victim := list[oldestIndex]
	list = append(list[:oldestIndex], list[oldestIndex+1:]...)
	if len(list) == 0 {
		delete(p.idle, oldestKey)
	} else {
		p.idle[oldestKey] = list
	}
	_ = victim.res.close()
	p.releaseSlot()
}

// Close closes every idle connection and marks the pool shutting down.
func (p *resourcePool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	idle := p.idle
	p.idle = make(map[connKey][]idleResource)
	p.mu.Unlock()
	for _, list := range idle {
		for _, item := range list {
			_ = item.res.close()
			p.releaseSlot()
		}
	}
}
