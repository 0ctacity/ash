package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ashmcp "ash/internal/mcp"
)

func TestHostsDoesNotRequireSSHCredentials(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, []byte("[hosts.h]\naddress='localhost'\nuser='test'\nidentity='/secret/key'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	var out, errout bytes.Buffer
	code := Run(context.Background(), []string{"--config", cfg, "hosts"}, strings.NewReader(""), &out, &errout)
	if code != 0 || !strings.Contains(out.String(), `"name":"h"`) || strings.Contains(out.String(), "secret") {
		t.Fatalf("%d %s %s", code, out.String(), errout.String())
	}
}

func TestVersionDoesNotRequireConfig(t *testing.T) {
	var out, errout bytes.Buffer
	code := Run(context.Background(), []string{"--version"}, strings.NewReader(""), &out, &errout)
	if code != 0 || out.String() != "ash "+ashmcp.Version+"\n" || errout.Len() != 0 {
		t.Fatalf("%d %q %q", code, out.String(), errout.String())
	}
}
