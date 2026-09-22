package ssh

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeResource implements the resource contract with configurable behavior.
type fakeResource struct {
	aliveVal    bool
	closeCount  int
	healthCount atomic.Int64
	blockHealth chan struct{} // when set, healthCheck blocks until closed
	lifecycle   resourceLifecycle
	closeOnce   sync.Once
}

func (f *fakeResource) healthCheck(ctx context.Context) bool {
	f.healthCount.Add(1)
	if f.blockHealth != nil {
		<-ctx.Done()
		return false
	}
	return f.aliveVal
}

func (f *fakeResource) close() error {
	f.closeOnce.Do(func() {
		f.closeCount++
		if f.lifecycle.cleanup != nil {
			f.lifecycle.cleanup()
			f.lifecycle = resourceLifecycle{}
		}
	})
	return nil
}

func (f *fakeResource) setLifecycle(lc resourceLifecycle) { f.lifecycle = lc }

func newTestPool(maxConn, maxIdle int, ttl time.Duration) *resourcePool {
	p := newResourcePool(maxConn, maxIdle, ttl, nil)
	p.healthTimeout = 50 * time.Millisecond
	return p
}

func TestPoolGetPutRoundTrip(t *testing.T) {
	p := newTestPool(4, 2, time.Minute)
	key := connKey{address: "h1"}
	res := &fakeResource{aliveVal: true}
	if _, ok := p.get(t.Context(), key); ok {
		t.Fatal("expected empty pool miss")
	}
	p.reserve(t.Context())
	p.put(key, res)
	got, ok := p.get(t.Context(), key)
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
	if _, ok := p.get(t.Context(), key); ok {
		t.Fatal("dead resource must not be reused")
	}
	if dead.closeCount != 1 {
		t.Fatalf("dead resource should be closed once, got %d", dead.closeCount)
	}
	if dead.healthCount.Load() != 1 {
		t.Fatalf("failed health check should run exactly once, got %d", dead.healthCount.Load())
	}
	expired := &fakeResource{aliveVal: true}
	p.reserve(t.Context())
	p.put(key, expired)
	p.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, ok := p.get(t.Context(), key); ok {
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
	late := &fakeResource{aliveVal: true}
	p.put(key, late)
	if late.closeCount != 1 {
		t.Fatal("resource put after Close must be closed, not pooled")
	}
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
	got, ok := p.get(t.Context(), key)
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

// TestPoolBlockedHealthCheckDoesNotStallOtherKeys is the core regression: a
// half-open connection whose health check never completes must not hold the
// pool mutex, so another key can be checked and returned concurrently.
func TestPoolBlockedHealthCheckDoesNotStallOtherKeys(t *testing.T) {
	p := newTestPool(8, 4, time.Minute)
	stuckKey := connKey{address: "stuck"}
	otherKey := connKey{address: "other"}

	stuck := &fakeResource{aliveVal: true, blockHealth: make(chan struct{})}
	other := &fakeResource{aliveVal: true}
	p.reserve(t.Context())
	p.put(stuckKey, stuck)
	p.reserve(t.Context())
	p.put(otherKey, other)

	// Start a get for the stuck key; its health check blocks (bounded) without
	// the mutex held.
	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = p.get(context.Background(), stuckKey)
	}()
	<-started

	// Concurrently, the other key must be served.
	done := make(chan resource, 1)
	go func() {
		res, ok := p.get(context.Background(), otherKey)
		if ok {
			done <- res
		}
	}()
	select {
	case res := <-done:
		if res != resource(other) {
			t.Fatalf("unexpected resource: %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a blocked health check for one key stalled another key")
	}

	// Shutdown must not wait behind the stalled probe either.
	closed := make(chan struct{})
	go func() {
		p.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close deadlocked behind a stalled health check")
	}
}

// TestPoolHealthCheckHonorsCallerCancellation: cancelling the caller's context
// aborts the wait for a stalled probe and reports the resource as unusable.
func TestPoolHealthCheckHonorsCallerCancellation(t *testing.T) {
	p := newTestPool(4, 2, time.Minute)
	p.healthTimeout = time.Hour // only the caller's cancel can stop the probe
	key := connKey{address: "h1"}
	stuck := &fakeResource{aliveVal: true, blockHealth: make(chan struct{})}
	p.reserve(t.Context())
	p.put(key, stuck)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Synchronize on the probe actually starting.
		for stuck.healthCount.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	if _, ok := p.get(ctx, key); ok {
		t.Fatal("cancelled health check must not return the resource")
	}
	if stuck.closeCount != 1 {
		t.Fatalf("cancelled resource should be discarded once, got %d closes", stuck.closeCount)
	}
}

// TestPoolHealthCheckTimeoutBoundsStalledProbe: a probe that never completes
// is abandoned after the pool's explicit timeout and its connection discarded.
func TestPoolHealthCheckTimeoutBoundsStalledProbe(t *testing.T) {
	p := newTestPool(4, 2, time.Minute)
	p.healthTimeout = 30 * time.Millisecond
	key := connKey{address: "h1"}
	stuck := &fakeResource{aliveVal: true, blockHealth: make(chan struct{})}
	p.reserve(t.Context())
	p.put(key, stuck)

	begin := time.Now()
	if _, ok := p.get(context.Background(), key); ok {
		t.Fatal("stalled probe must fail the health check")
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("health check waited %v; pool timeout must bound it", elapsed)
	}
	if stuck.closeCount != 1 {
		t.Fatalf("timed-out resource should be discarded once, got %d closes", stuck.closeCount)
	}
}

// TestPoolSlotAccountingUnderChurn: dead, expired, evicted, and shutdown
// closes must all release their slot exactly once, so the pool never leaks
// capacity.
func TestPoolSlotAccountingUnderChurn(t *testing.T) {
	p := newTestPool(2, 2, time.Minute)
	key := connKey{address: "h1"}

	// Dead resource: slot must be released so the next reserve succeeds.
	dead := &fakeResource{aliveVal: false}
	p.reserve(t.Context())
	p.put(key, dead)
	p.get(context.Background(), key)
	if err := p.reserve(t.Context()); err != nil {
		t.Fatalf("slot leaked by dead-resource discard: %v", err)
	}
	p.releaseSlot()

	// Expired resource: same expectation.
	expired := &fakeResource{aliveVal: true}
	p.reserve(t.Context())
	p.put(key, expired)
	p.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	p.get(context.Background(), key)
	if err := p.reserve(t.Context()); err != nil {
		t.Fatalf("slot leaked by expired-resource discard: %v", err)
	}
	p.releaseSlot()
}

// TestPoolShutdownClosesAndReleasesAllSlots pins Close's contract: every idle
// resource is closed exactly once and every slot is released.
func TestPoolShutdownClosesAndReleasesAllSlots(t *testing.T) {
	const maxConn = 4
	p := newTestPool(maxConn, maxConn, time.Minute)
	keyA := connKey{address: "a"}
	keyB := connKey{address: "b"}
	a := &fakeResource{aliveVal: true}
	b := &fakeResource{aliveVal: true}
	p.reserve(t.Context())
	p.put(keyA, a)
	p.reserve(t.Context())
	p.put(keyB, b)
	p.Close()
	if a.closeCount != 1 || b.closeCount != 1 {
		t.Fatalf("shutdown must close each idle resource once: a=%d b=%d", a.closeCount, b.closeCount)
	}
	// Every slot taken before shutdown must be released again.
	if len(p.slots) != 0 {
		t.Fatalf("shutdown released %d of %d slots", maxConn-len(p.slots), maxConn)
	}
}

// TestPoolConcurrentAccessAcrossKeys hammers the pool from many goroutines on
// different keys while one key's health checks always stall; with -race this
// exposes mutex-held probes or slot double-release.
func TestPoolConcurrentAccessAcrossKeys(t *testing.T) {
	p := newTestPool(16, 2, time.Minute)
	stuckKey := connKey{address: "stuck"}
	stuck := &fakeResource{aliveVal: true, blockHealth: make(chan struct{})}
	p.reserve(t.Context())
	p.put(stuckKey, stuck)

	var wg sync.WaitGroup
	var successes atomic.Int64
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := connKey{address: "k", port: i}
			for {
				select {
				case <-stop:
					return
				default:
				}
				res := &fakeResource{aliveVal: true}
				if err := p.reserve(context.Background()); err != nil {
					return
				}
				p.put(key, res)
				if got, ok := p.get(context.Background(), key); ok {
					successes.Add(1)
					_ = resource(got).close()
					p.releaseSlot()
				}
			}
		}(i)
	}
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
	if successes.Load() == 0 {
		t.Fatal("concurrent traffic made no progress despite a stalled key")
	}
	p.Close()
}
