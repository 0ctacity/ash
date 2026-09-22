package integration

import (
	"ash/internal/sshconfig"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestOpenSSHAliasResolution exercises the real ssh -G path, including Include
// and Match. It is opt-in because it requires the OpenSSH client.
func TestOpenSSHAliasResolution(t *testing.T) {
	if os.Getenv("ASH_INTEGRATION") != "1" {
		t.Skip("set ASH_INTEGRATION=1 to run real OpenSSH alias resolution")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("OpenSSH client not installed")
	}
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "extra.conf"), []byte("Host include-target\n  HostName include.example\n  User includeuser\n  Port 2200\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := "Include extra.conf\n" +
		"Host alias\n  HostName alias.example\n  User aliasuser\n  IdentityFile ~/.ssh/id_one\n  IdentityFile ~/.ssh/id_two\n" +
		"Match host alias\n  Port 2222\n"
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	effective, err := sshconfig.Resolve(context.Background(), "alias")
	if err != nil {
		t.Fatal(err)
	}
	if effective.HostName != "alias.example" || effective.User != "aliasuser" || effective.Port != 2222 {
		t.Fatalf("%+v", effective)
	}
	if len(effective.Identities) != 2 {
		t.Fatalf("identities: %+v", effective)
	}
	included, err := sshconfig.Resolve(context.Background(), "include-target")
	if err != nil || included.HostName != "include.example" || included.User != "includeuser" || included.Port != 2200 {
		t.Fatalf("%+v %v", included, err)
	}
}
