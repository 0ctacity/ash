package ssh

import (
	"testing"
	"time"
)

type fakeResource struct {
	aliveVal   bool
	closeCount int
	lifecycle  resourceLifecycle
}

func (f *fakeResource) alive() bool { return f.aliveVal }
func (f *fakeResource) close() error {
	f.closeCount++
	if f.lifecycle.cleanup != nil {
		f.lifecycle.cleanup()
		f.lifecycle = resourceLifecycle{}
	}
	return nil
}
func (f *fakeResource) setLifecycle(lc resourceLifecycle) { f.lifecycle = lc }

func newTestPool(maxConn, maxIdle int, ttl time.Duration) *resourcePool {
	return newResourcePool(maxConn, maxIdle, ttl, nil)
}

func TestPoolGetPutRoundTrip(t *testing.T) {
	p := newTestPool(4, 2, time.Minute)
	key := connKey{address: "h1"}
	res := &fakeResource{aliveVal: true}
	if _, ok := p.get(key); ok {
		t.Fatal("expected empty pool miss")
	}
	p.reserve(t.Context())
	p.put(key, res)
	got, ok := p.get(key)
	if !ok || got != resource(res) {
		t.Fatalf("expected pooled resource, got %+v ok=%v", got, ok)
	}
}

func TestPoolEvictsDeadAndExpired(t *testing.T) {
	p := newTestPool(4, 2, time.Minute)
	key := connKey{address: "h1"}
	dead := &fakeResource{aliveVal: false}
	p.reserve(t.Context())
	p.put(key, dead)
	if _, ok := p.get(key); ok {
		t.Fatal("dead resource must not be reused")
	}
	if dead.closeCount != 1 {
		t.Fatalf("dead resource should be closed once, got %d", dead.closeCount)
	}
	expired := &fakeResource{aliveVal: true}
	p.reserve(t.Context())
	p.put(key, expired)
	p.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, ok := p.get(key); ok {
		t.Fatal("expired resource must not be reused")
	}
	if expired.closeCount != 1 {
		t.Fatalf("expired resource should be closed once, got %d", expired.closeCount)
	}
}

func TestPoolMaxIdlePerKey(t *testing.T) {
	p := newTestPool(8, 1, time.Minute)
	key := connKey{address: "h1"}
	first := &fakeResource{aliveVal: true}
	second := &fakeResource{aliveVal: true}
	p.reserve(t.Context())
	p.put(key, first)
	p.reserve(t.Context())
	p.put(key, second)
	if first.closeCount != 0 {
		t.Fatal("first resource should stay pooled")
	}
	if second.closeCount != 1 {
		t.Fatal("second resource should be closed when idle bound is reached")
	}
}

func TestPoolBoundsTotalConnections(t *testing.T) {
	p := newTestPool(1, 1, time.Minute)
	keyA := connKey{address: "a"}
	keyB := connKey{address: "b"}
	a := &fakeResource{aliveVal: true}
	p.reserve(t.Context())
	p.put(keyA, a)
	// The single slot is occupied by the idle connection; a second reserve
	// must evict the idle one instead of blocking forever.
	if err := p.reserve(t.Context()); err != nil {
		t.Fatalf("reserve after eviction: %v", err)
	}
	if a.closeCount != 1 {
		t.Fatalf("idle connection should be evicted to free a slot, closed %d times", a.closeCount)
	}
	p.put(keyB, &fakeResource{aliveVal: true})
}

func TestPoolCloseShutsDownIdle(t *testing.T) {
	p := newTestPool(4, 2, time.Minute)
	key := connKey{address: "h1"}
	res := &fakeResource{aliveVal: true}
	p.reserve(t.Context())
	p.put(key, res)
	p.Close()
	if res.closeCount != 1 {
		t.Fatalf("Close should close idle resources, got %d", res.closeCount)
	}
	if err := p.reserve(t.Context()); err == nil {
		t.Fatal("reserve after Close must fail")
	}
	p.put(key, &fakeResource{aliveVal: true})
	p.Close()
}

func TestPoolTrackResourcesRunsCleanupOnce(t *testing.T) {
	p := newTestPool(4, 2, time.Minute)
	key := connKey{address: "h1"}
	cleanups := 0
	res := &fakeResource{aliveVal: true}
	res.setLifecycle(resourceLifecycle{stop: func() bool { return true }, cleanup: func() { cleanups++ }})
	p.reserve(t.Context())
	p.put(key, res)
	got, ok := p.get(key)
	if !ok {
		t.Fatal("expected pooled resource")
	}
	_ = got.close()
	if cleanups != 1 {
		t.Fatalf("cleanup should run exactly once, ran %d times", cleanups)
	}
	_ = got.close()
	if cleanups != 1 {
		t.Fatalf("cleanup must not run twice, ran %d times", cleanups)
	}
}
