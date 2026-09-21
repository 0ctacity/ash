package integration

import (
	"ash/internal/config"
	"ash/internal/host"
	sshtransport "ash/internal/transport/ssh"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// uniqueRemotePath returns a fresh path under the user's home directory so
// repeated runs never collide and leftovers never matter.
func uniqueRemotePath(t *testing.T, name string) string {
	t.Helper()
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(filepath.Join(home, ".cache", "ash-integration-"+hex.EncodeToString(random[:]), name))
}

// cleanupRemote removes the path's parent directory (created by the test)
// through SFTP, so nothing remains even after a failed test body.
func cleanupRemote(t *testing.T, tr *sshtransport.Transport, h host.Host, p string) {
	t.Helper()
	dir := filepath.ToSlash(filepath.Dir(p))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Remove the file first; the parent directory is then empty and removed.
	tr.Remove(ctx, h, p)
	tr.Remove(ctx, h, dir)
}

// TestFedoraSFTPReplacement is the opt-in test against a real configured
// OpenSSH server. It pins the posix-rename@openssh.com behavior that the
// in-process fixture cannot prove end-to-end: replacing an existing file
// atomically on OpenSSH, where a plain SFTP RENAME fails with SSH_FX_FAILURE.
//
// Enable with ASH_SFTP_HOST=<host name from the ASH config>:
//
//	ASH_SFTP_HOST=fedora go test -v ./integration -run TestFedoraSFTPReplacement
//
// The test only touches a unique path under ~/.cache/ash-integration-<random>
// and removes it afterwards, even when it fails.
func TestFedoraSFTPReplacement(t *testing.T) {
	name := os.Getenv("ASH_SFTP_HOST")
	if name == "" {
		t.Skip("set ASH_SFTP_HOST=<host> to run the real-server SFTP replacement test")
	}
	if os.Getenv("ASH_CONFIG") == "" {
		t.Skip("set ASH_CONFIG=<path to ash config.toml> alongside ASH_SFTP_HOST")
	}
	// Load the configured host through the same config package the CLI uses,
	// so this test exercises the exact identity and trust settings.
	configPath := os.Getenv("ASH_CONFIG")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configured, ok := cfg.Hosts[name]
	if !ok {
		t.Fatalf("host %q is not configured in %s", name, configPath)
	}
	if !configured.Policy.Write {
		t.Fatal("fixture requires explicitly enabled write policy")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	tr, err := sshtransport.New(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	h := configured

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	target := uniqueRemotePath(t, "atomic.txt")
	t.Cleanup(func() { cleanupRemote(t, tr, h, target) })

	// 1. Initial creation into a missing directory.
	if err := tr.AtomicWrite(ctx, h, target, []byte("first")); err != nil {
		t.Fatalf("initial atomic write into missing directory: %v", err)
	}
	assertRemoteContent(t, ctx, tr, h, target, "first")

	// 2. Replacement of an existing file: the case that failed with
	// SSH_FX_FAILURE before posix-rename was used.
	if err := tr.AtomicWrite(ctx, h, target, []byte("second")); err != nil {
		t.Fatalf("atomic replacement over existing file: %v", err)
	}
	assertRemoteContent(t, ctx, tr, h, target, "second")

	// 3. No temporary files were left behind in the target's directory.
	entries, err := tr.List(ctx, h, filepath.ToSlash(filepath.Dir(target)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, ".ash-tmp-") {
			t.Fatalf("temporary file left behind on the remote host: %s", entry.Name)
		}
	}
}

func assertRemoteContent(t *testing.T, ctx context.Context, tr *sshtransport.Transport, h host.Host, p, want string) {
	t.Helper()
	data, err := tr.Read(ctx, h, p)
	if err != nil || string(data) != want {
		t.Fatalf("content of %s = %q err %v, want %q", p, data, err, want)
	}
}
