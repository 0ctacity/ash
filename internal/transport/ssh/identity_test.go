package ssh

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ash/internal/host"
)

// 64-byte ed25519 private seed (two 32-byte halves), the smallest valid
// representation gossh.ParsePrivateKey accepts in OpenSSH PEM form.
var testKeyPEM = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACBBYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ejAxMjM0NTYAAAAIbmlzdHA1
MTIAAAEFFwAAAAtzc2gtZWQyNTUxOQAAAEBBYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ej
AxMjM0NTY=
-----END OPENSSH PRIVATE KEY-----
`

func TestSelectIdentitiesExplicitStaysStrict(t *testing.T) {
	h := host.Host{Identity: "~/.ssh/id_ed25519"}
	paths, explicit := selectIdentities(h)
	if !explicit {
		t.Fatal("explicit ASH identity must be marked strict")
	}
	if len(paths) != 1 || paths[0] != "~/.ssh/id_ed25519" {
		t.Fatalf("unexpected paths %v", paths)
	}
}

func TestSelectIdentitiesResolvedAreLenient(t *testing.T) {
	h := host.Host{Identities: []string{"~/.ssh/id_rsa", "~/.ssh/id_ed25519"}}
	paths, explicit := selectIdentities(h)
	if explicit {
		t.Fatal("ssh -G resolved identities must not be strict")
	}
	if len(paths) != 2 {
		t.Fatalf("unexpected paths %v", paths)
	}
}

func TestLoadIdentityFileSkipsMissingWhenAllowed(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "id_rsa")
	if _, err := loadIdentityFile(missing, true); !errors.Is(err, errIdentityMissing) {
		t.Fatalf("missingOK=true should yield errIdentityMissing, got %v", err)
	}
	if _, err := loadIdentityFile(missing, false); err == nil || errors.Is(err, errIdentityMissing) {
		t.Fatalf("missingOK=false must fail strictly, got %v", err)
	}
}

func TestLoadIdentityFileRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "id_bad")
	if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadIdentityFile(path, true); err == nil || errors.Is(err, errIdentityMissing) {
		t.Fatalf("unparseable key must be an error, got %v", err)
	}
}
