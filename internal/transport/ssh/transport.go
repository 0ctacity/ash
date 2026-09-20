// Package ssh implements a verified SSH transport whose connections are pooled
// and reused across operations. Connections are keyed by host and all
// authentication material, health-checked before reuse, and bounded by idle
// limits and lifetime.
package ssh

import (
	"ash/internal/host"
	"ash/internal/transport"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Transport struct {
	knownHostsPath string
	pool           *resourcePool
}

func New(knownHostsPath string) (*Transport, error) {
	return &Transport{
		knownHostsPath: knownHostsPath,
		pool:           newResourcePool(DefaultMaxConnections, DefaultMaxIdlePerHost, DefaultIdleLifetime, nil),
	}, nil
}

// Close shuts down the connection pool, closing every idle connection. Active
// operations keep their own connection until they finish.
func (t *Transport) Close() { t.pool.Close() }

// keyFor derives the reuse boundary for a host. Host, authentication material,
// agent, host-key alias, and trust file must all match.
func keyFor(h host.Host, knownHostsPath string) connKey {
	return connKey{
		address:      h.Address,
		port:         h.Port,
		user:         h.User,
		identity:     h.Identity,
		identities:   strings.Join(h.Identities, "\x00"),
		agent:        h.AgentSocket,
		hostKeyAlias: h.HostKeyAlias,
		knownHosts:   knownHostsPath,
	}
}

// sshResource adapts a pooled SSH client to the pool's resource contract.
type sshResource struct {
	client    *gossh.Client
	lifecycle resourceLifecycle
}

func (r *sshResource) alive() bool {
	if r.client == nil {
		return false
	}
	_, _, err := r.client.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

func (r *sshResource) setLifecycle(lc resourceLifecycle) { r.lifecycle = lc }

func (r *sshResource) close() error {
	if r.client == nil {
		return nil
	}
	return r.client.Close()
}

func (t *Transport) knownHosts() (gossh.HostKeyCallback, error) {
	callback, err := knownhosts.New(t.knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("%w: load known_hosts: %v", transport.ErrHostKey, err)
	}
	return func(address string, remote net.Addr, key gossh.PublicKey) error {
		if err := callback(address, remote, key); err != nil {
			return fmt.Errorf("%w: %s is not trusted; verify it using OpenSSH and update known_hosts: %w", transport.ErrHostKey, address, err)
		}
		return nil
	}, nil
}

func operationError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", transport.ErrTimeout, ctx.Err())
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// expandHome resolves a leading ~ against the local home directory.
func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// connect returns a pooled or freshly dialed client. The returned release
// function returns the connection to the pool for reuse; it never closes a
// healthy connection. Connection lifetime is intentionally independent of the
// per-operation context.
func (t *Transport) connect(ctx context.Context, h host.Host) (*gossh.Client, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, operationError(ctx, err)
	}
	key := keyFor(h, t.knownHostsPath)
	if res, ok := t.pool.get(key); ok {
		return res.(*sshResource).client, func() { t.pool.put(key, res) }, nil
	}
	if err := t.pool.reserve(ctx); err != nil {
		return nil, nil, operationError(ctx, err)
	}
	client, err := t.dial(ctx, h)
	if err != nil {
		t.pool.releaseSlot()
		return nil, nil, err
	}
	res := &sshResource{client: client}
	return client, func() { t.pool.put(key, res) }, nil
}

// dial establishes a fresh, verified connection. The returned cleanup closes
// the connection and any agent socket it opened; it is only valid for
// connections that fail before entering the pool. A pooled connection is never
// closed by the caller; release returns it to the pool instead.
func (t *Transport) dial(ctx context.Context, h host.Host) (*gossh.Client, error) {
	hostKey, err := t.knownHosts()
	if err != nil {
		return nil, err
	}
	var agentConn net.Conn
	var stopAgent func() bool
	cleanupAgent := func() {
		if stopAgent != nil {
			stopAgent()
		}
		if agentConn != nil {
			agentConn.Close()
		}
	}
	var allSigners []gossh.Signer
	// An explicit OpenSSH IdentityAgent (including "none") overrides the
	// environment; otherwise use SSH_AUTH_SOCK.
	agentSocket := h.AgentSocket
	if agentSocket == "" {
		agentSocket = os.Getenv("SSH_AUTH_SOCK")
	}
	if agentSocket != "" && agentSocket != "none" {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", agentSocket)
		if err == nil {
			agentConn = conn
			stopAgent = context.AfterFunc(ctx, func() { conn.Close() })
			signers, signErr := agent.NewClient(conn).Signers()
			if signErr == nil && len(signers) > 0 {
				allSigners = append(allSigners, signers...)
			}
		}
	}
	identities := h.Identities
	if len(identities) == 0 && h.Identity != "" {
		identities = []string{h.Identity}
	}
	for _, identity := range identities {
		path, err := expandHome(identity)
		if err != nil {
			cleanupAgent()
			return nil, err
		}
		key, err := os.ReadFile(path)
		if err != nil {
			cleanupAgent()
			return nil, fmt.Errorf("%w: read identity: %v", transport.ErrAuthentication, err)
		}
		signer, err := gossh.ParsePrivateKey(key)
		if err != nil {
			cleanupAgent()
			return nil, fmt.Errorf("%w: parse identity: %v", transport.ErrAuthentication, err)
		}
		allSigners = append(allSigners, signer)
	}
	if ctx.Err() != nil {
		cleanupAgent()
		return nil, operationError(ctx, ctx.Err())
	}
	if len(allSigners) == 0 {
		cleanupAgent()
		return nil, fmt.Errorf("%w: no SSH agent keys or identity available", transport.ErrAuthentication)
	}
	port := h.Port
	if port == 0 {
		port = 22
	}
	dialAddress := net.JoinHostPort(h.Address, strconv.Itoa(port))
	// HostKeyAlias changes only host-key lookup, not the dialed address.
	verifyAddress := dialAddress
	if h.HostKeyAlias != "" {
		verifyAddress = net.JoinHostPort(h.HostKeyAlias, strconv.Itoa(port))
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", dialAddress)
	if err != nil {
		cleanupAgent()
		return nil, operationError(ctx, fmt.Errorf("connect to host %q: %w", h.Name, err))
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	cleanup := func() { stop(); conn.Close(); cleanupAgent() }
	algorithms := preferredHostKeyAlgorithms(hostKey, verifyAddress, conn.RemoteAddr())
	sshConn, chans, reqs, err := gossh.NewClientConn(conn, verifyAddress, &gossh.ClientConfig{User: h.User, Auth: []gossh.AuthMethod{gossh.PublicKeys(allSigners...)}, HostKeyCallback: hostKey, HostKeyAlgorithms: algorithms})
	if err != nil {
		cleanup()
		err = operationError(ctx, err)
		if strings.Contains(err.Error(), "unable to authenticate") {
			err = fmt.Errorf("%w: %v", transport.ErrAuthentication, err)
		}
		return nil, err
	}
	client := gossh.NewClient(sshConn, chans, reqs)
	// Once the handshake succeeds the per-dial resources belong to the
	// long-lived connection: the context stop and agent socket must stay alive
	// until the connection itself closes.
	res := &sshResource{client: client}
	t.pool.trackResources(res, stop, cleanup)
	return client, nil
}

// preferredHostKeyAlgorithms asks the known_hosts matcher for this host's keys
// using a deliberately nonmatching probe. This preserves its hashed-name, port,
// wildcard, and revocation semantics. Actual server keys are still verified by
// the unchanged callback during the handshake.
func preferredHostKeyAlgorithms(callback gossh.HostKeyCallback, address string, remote net.Addr) []string {
	probe, _ := gossh.NewPublicKey(ed25519.PublicKey(make([]byte, ed25519.PublicKeySize)))
	var keyError *knownhosts.KeyError
	err := callback(address, remote, probe)
	if !errors.As(err, &keyError) || len(keyError.Want) == 0 {
		return nil
	}
	trusted := make(map[string]bool)
	for _, known := range keyError.Want {
		trusted[known.Key.Type()] = true
		if known.Key.Type() == gossh.KeyAlgoRSA {
			trusted[gossh.KeyAlgoRSASHA256] = true
			trusted[gossh.KeyAlgoRSASHA512] = true
		}
	}
	supported := gossh.SupportedAlgorithms().HostKeys
	ordered := make([]string, 0, len(supported))
	for _, algorithm := range supported {
		if trusted[algorithm] {
			ordered = append(ordered, algorithm)
		}
	}
	for _, algorithm := range supported {
		if !trusted[algorithm] {
			ordered = append(ordered, algorithm)
		}
	}
	return ordered
}
