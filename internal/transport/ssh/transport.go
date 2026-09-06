// Package ssh implements one verified SSH connection per remote operation.
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

type Transport struct{ knownHostsPath string }

func New(knownHostsPath string) (*Transport, error) {
	return &Transport{knownHostsPath: knownHostsPath}, nil
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
func (t *Transport) connect(ctx context.Context, h host.Host) (*gossh.Client, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, operationError(ctx, err)
	}
	hostKey, err := t.knownHosts()
	if err != nil {
		return nil, nil, err
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
	if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err == nil {
			agentConn = conn
			stopAgent = context.AfterFunc(ctx, func() { conn.Close() })
			signers, signErr := agent.NewClient(conn).Signers()
			if signErr == nil && len(signers) > 0 {
				allSigners = append(allSigners, signers...)
			}
		}
	}
	if h.Identity != "" {
		identity := h.Identity
		if strings.HasPrefix(identity, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				cleanupAgent()
				return nil, nil, err
			}
			identity = filepath.Join(home, identity[2:])
		}
		key, err := os.ReadFile(identity)
		if err != nil {
			cleanupAgent()
			return nil, nil, fmt.Errorf("%w: read identity: %v", transport.ErrAuthentication, err)
		}
		signer, err := gossh.ParsePrivateKey(key)
		if err != nil {
			cleanupAgent()
			return nil, nil, fmt.Errorf("%w: parse identity: %v", transport.ErrAuthentication, err)
		}
		allSigners = append(allSigners, signer)
	}
	if ctx.Err() != nil {
		cleanupAgent()
		return nil, nil, operationError(ctx, ctx.Err())
	}
	if len(allSigners) == 0 {
		cleanupAgent()
		return nil, nil, fmt.Errorf("%w: no SSH agent keys or identity available", transport.ErrAuthentication)
	}
	port := h.Port
	if port == 0 {
		port = 22
	}
	address := net.JoinHostPort(h.Address, strconv.Itoa(port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		cleanupAgent()
		return nil, nil, operationError(ctx, fmt.Errorf("connect to host %q: %w", h.Name, err))
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	cleanup := func() { stop(); conn.Close(); cleanupAgent() }
	algorithms := preferredHostKeyAlgorithms(hostKey, address, conn.RemoteAddr())
	sshConn, chans, reqs, err := gossh.NewClientConn(conn, address, &gossh.ClientConfig{User: h.User, Auth: []gossh.AuthMethod{gossh.PublicKeys(allSigners...)}, HostKeyCallback: hostKey, HostKeyAlgorithms: algorithms})
	if err != nil {
		cleanup()
		err = operationError(ctx, err)
		if strings.Contains(err.Error(), "unable to authenticate") {
			err = fmt.Errorf("%w: %v", transport.ErrAuthentication, err)
		}
		return nil, nil, err
	}
	client := gossh.NewClient(sshConn, chans, reqs)
	return client, func() { client.Close(); cleanup() }, nil
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
