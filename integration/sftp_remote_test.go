package integration

import (
	"ash/internal/config"
	"ash/internal/host"
	sshtransport "ash/internal/transport/ssh"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFedoraSFTPReplacement is the opt-in test against a real configured
// OpenSSH server. It pins the posix-rename@openssh.com behavior that the
// in-process fixture cannot prove end-to-end: replacing an existing file
// atomically on OpenSSH, where a plain SFTP RENAME fails with SSH_FX_FAILURE.
//
// Enable with ASH_SFTP_HOST=<host name from the ASH config>:
//
//	ASH_SFTP_HOST=fedora ASH_CONFIG=<path to config.toml> \
//	  go test -v ./integration -run TestFedoraSFTPReplacement
//
// Every remote path is written as "~/.cache/ash-integration-<random>", which
// the transport resolves against the REMOTE user's home via its SFTP working
// directory. No local home directory is ever assumed to exist remotely, the
// test directory is created before the first write (AtomicWrite does not
// create parent directories), and cleanup removes everything through SFTP
// even when the test fails.
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
	// known_hosts defaults to ~/.ssh/known_hosts and can be redirected with
	// ASH_KNOWN_HOSTS, mirroring ASH_CONFIG, so the test can run against an
	// isolated trust store.
	knownHosts := os.Getenv("ASH_KNOWN_HOSTS")
	if knownHosts == "" {
		knownHosts = filepath.Join(homeDir(), ".ssh", "known_hosts")
	}
	tr, err := sshtransport.New(knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	h := configured

	// A unique directory under the remote ~/.cache, addressed with a
	// "~"-prefixed path so the remote side resolves it. The random suffix
	// means repeated runs and parallel users never share a path.
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	dir := "~/.cache/ash-integration-" + hex.EncodeToString(random[:])
	target := dir + "/atomic.txt"

	// Remove the test file and the whole test directory through SFTP. Runs
	// even when the test body fails; failures are only surfaced when the
	// main test otherwise succeeded, so they cannot mask a real failure.
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		cleanupErr := removeRemoteTree(cctx, tr, h, dir)
		if cleanupErr != nil {
			if t.Failed() {
				t.Logf("cleanup of %s failed: %v", dir, cleanupErr)
			} else {
				t.Errorf("cleanup of %s failed: %v", dir, cleanupErr)
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// AtomicWrite does not create parent directories, so the unique test
	// directory is created explicitly before the first write. The ~/.cache
	// base is created only if missing; Mkdir on an existing directory fails
	// harmlessly and the unique child below still pins the real requirement.
	_ = tr.Mkdir(ctx, h, "~/.cache")
	if err := tr.Mkdir(ctx, h, dir); err != nil {
		t.Fatalf("create unique remote test directory %s: %v", dir, err)
	}

	// 1. Initial creation.
	if err := tr.AtomicWrite(ctx, h, target, []byte("first")); err != nil {
		t.Fatalf("initial atomic write: %v", err)
	}
	assertRemoteContent(t, ctx, tr, h, target, "first")

	// 2. Replacement of an existing file: the case that failed with
	// SSH_FX_FAILURE before posix-rename was used.
	if err := tr.AtomicWrite(ctx, h, target, []byte("second")); err != nil {
		t.Fatalf("atomic replacement over existing file: %v", err)
	}
	assertRemoteContent(t, ctx, tr, h, target, "second")

	// 3. No temporary files were left behind in the target's directory.
	entries, err := tr.List(ctx, h, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, ".ash-tmp-") {
			t.Fatalf("temporary file left behind on the remote host: %s", entry.Name)
		}
	}
}

// removeRemoteTree deletes every entry of dir and then dir itself, so a
// leftover temporary file cannot block cleanup.
func removeRemoteTree(ctx context.Context, tr *sshtransport.Transport, h host.Host, dir string) error {
	entries, err := tr.List(ctx, h, dir)
	if err != nil {
		return fmt.Errorf("list %s: %w", dir, err)
	}
	var firstErr error
	for _, entry := range entries {
		if err := tr.Remove(ctx, h, entry.Path); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("remove %s: %w", entry.Path, err)
		}
	}
	if err := tr.Remove(ctx, h, dir); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("remove %s: %w", dir, err)
	}
	return firstErr
}

func assertRemoteContent(t *testing.T, ctx context.Context, tr *sshtransport.Transport, h host.Host, p, want string) {
	t.Helper()
	data, err := tr.Read(ctx, h, p)
	if err != nil || string(data) != want {
		t.Fatalf("content of %s = %q err %v, want %q", p, data, err, want)
	}
}

// homeDir returns the LOCAL home directory, used only for local files such as
// the known-hosts store. Remote paths must never be derived from it.
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
