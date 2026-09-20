package ssh

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ash/internal/host"
)

func hostForTest(name string) host.Host {
	return host.Host{Name: name, Address: "127.0.0.1", Port: 22, User: "u"}
}

// Recording resources to observe what the pool actually receives.

// newWiringHarness builds a Transport whose dial is replaced, so tests can
// observe the exact resource instance that flows into the pool.
func newWiringHarness(t *testing.T) (*Transport, *resourcePool) {
	t.Helper()
	tr, err := New(filepath.Join(t.TempDir(), "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.Close)
	return tr, tr.pool
}

// The production bug this test pins: connect must pool the exact resource
// dial returned, and that resource must carry the lifecycle. If connect
// fabricates its own sshResource, the lifecycle is lost and the operation
// context (via AfterFunc) closes the socket after the first operation.
func TestConnectPoolsTheDialedResourceWithLifecycle(t *testing.T) {
	tr, pool := newWiringHarness(t)
	h := hostForTest("dial-wiring")
	key := keyFor(h, tr.knownHostsPath)

	dialed := &sshResource{client: nil}
	dialed.setLifecycle(resourceLifecycle{
		stop:    func() bool { return true },
		cleanup: func() {},
	})
	tr.setDial(func(ctx context.Context, h host.Host) (*sshResource, error) {
		return dialed, nil
	})

	client, release, err := tr.connect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	release()

	pool.mu.Lock()
	pooled := pool.idle[key]
	pool.mu.Unlock()
	if len(pooled) != 1 {
		t.Fatalf("expected 1 pooled resource, got %d", len(pooled))
	}
	if pooled[0].res != resource(dialed) {
		t.Fatal("connect pooled a different resource than dial returned: lifecycle would be discarded")
	}
	if client != dialed.client {
		t.Fatal("connect returned a client from a different resource")
	}
}

func TestOperationContextCancellationDoesNotClosePooledConnection(t *testing.T) {
	tr, _ := newWiringHarness(t)
	h := hostForTest("ctx-cancel")
	key := keyFor(h, tr.knownHostsPath)

	closed := make(chan struct{})
	dialed := &sshResource{client: nil}
	dialed.setLifecycle(resourceLifecycle{
		stop:    func() bool { return true },
		cleanup: func() { close(closed) },
	})
	tr.setDial(func(ctx context.Context, h host.Host) (*sshResource, error) {
		return dialed, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	client, release, err := tr.connect(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	// The operation context is done; a pooled connection must survive it.
	select {
	case <-closed:
		t.Fatal("operation context cancellation closed the pooled connection")
	case <-time.After(50 * time.Millisecond):
	}
	release()

	// The resource is back in the pool and still usable. A nil-client stub is
	// never "alive" by the keepalive contract, so read the idle set directly
	// instead of going through get, which would evict it.
	tr.pool.mu.Lock()
	pooled := tr.pool.idle[key]
	tr.pool.mu.Unlock()
	if len(pooled) != 1 || pooled[0].res != resource(dialed) {
		t.Fatalf("released resource was not returned to the pool intact: %+v", pooled)
	}
	_ = client
}
