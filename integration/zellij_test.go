package integration

import (
	"ash/internal/config"
	"ash/internal/shell"
	"ash/internal/shell/zellij"
	"ash/internal/transport"
	sshtransport "ash/internal/transport/ssh"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestZellij explicitly opts into creating and cleaning one isolated ASH session.
func TestZellij(t *testing.T) {
	name := os.Getenv("ASH_ZELLIJ_HOST")
	if name == "" {
		t.Skip("set ASH_ZELLIJ_HOST and optional ASH_ZELLIJ_CONFIG for authorized remote integration")
	}
	configPath := os.Getenv("ASH_ZELLIJ_CONFIG")
	if configPath == "" {
		configPath, _ = config.DefaultPath()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := cfg.Hosts[name]
	if !ok {
		t.Fatal("host not configured")
	}
	if !h.Policy.Exec {
		t.Fatal("fixture requires explicitly enabled exec policy")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	tr, err := sshtransport.New(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 16)
	rand.Read(raw)
	id := "sh_" + hex.EncodeToString(raw)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	backend := zellij.New(tr)
	cwd := "/tmp/ash-zellij-" + id + " space ' $(printf never)"
	r, err := tr.Exec(ctx, h, transport.ExecRequest{Command: "mkdir -- " + sshtransport.QuoteShell(cwd)})
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("mkdir: %+v %v", r, err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if e := zellij.New(tr).Close(cleanup, h, id); e != nil {
			t.Errorf("cleanup shell: %v", e)
		}
		r, e := tr.Exec(cleanup, h, transport.ExecRequest{Command: "rmdir -- " + sshtransport.QuoteShell(cwd)})
		if e != nil || r.ExitCode != 0 {
			t.Errorf("cleanup cwd: %+v %v", r, e)
		}
	})
	t.Log("creating isolated background session")
	if err = backend.Create(ctx, h, id, cwd); err != nil {
		t.Fatal(err)
	}
	// Fresh backends and short-lived transport connections must rediscover the shell.
	backend = zellij.New(tr)
	ids, err := backend.List(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range ids {
		if v == id {
			found = true
		}
	}
	if !found {
		t.Fatal(ids)
	}
	r, err = tr.Exec(ctx, h, transport.ExecRequest{Command: `zellij_bin=$(command -v zellij) || zellij_bin="$HOME/.cargo/bin/zellij"; "$zellij_bin" --session ` + sshtransport.QuoteShell("ash-"+id) + ` action list-panes --json`})
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("panes: %+v %v", r, err)
	}
	var panes []struct {
		IsPlugin bool `json:"is_plugin"`
	}
	if err = json.Unmarshal([]byte(r.Stdout), &panes); err != nil {
		t.Fatal(err)
	}
	if len(panes) != 1 || panes[0].IsPlugin {
		t.Fatalf("expected isolated terminal only: %s", r.Stdout)
	}
	input := "ASH_STATE=survives; printf '\\nASH_READY_%s\\n' 'literal $() quote'; pwd\n"
	if err = backend.Send(ctx, h, id, input); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		output, e := zellij.New(tr).Read(ctx, h, id)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(output.Content, "ASH_READY_literal $() quote") && strings.Contains(output.Content, cwd) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot: %q", output.Content)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err = zellij.New(tr).Send(ctx, h, id, "printf '\\nSTATE_%s\\n' \"$ASH_STATE\"\n"); err != nil {
		t.Fatal(err)
	}
	output, err := zellij.New(tr).Read(ctx, h, id)
	if err != nil || !strings.Contains(output.Content, "STATE_survives") {
		t.Fatalf("state: %+v %v", output, err)
	}
	t.Log("closing named session and verifying fresh liveness")
	if err = backend.Close(ctx, h, id); err != nil {
		t.Fatal(err)
	}
	if _, err = zellij.New(tr).Read(ctx, h, id); !errors.Is(err, shell.ErrNotFound) {
		t.Fatalf("closed session: %v", err)
	}
	if err = backend.Create(ctx, h, id, cwd); err != nil {
		t.Fatal(err)
	}
	r, err = tr.Exec(ctx, h, transport.ExecRequest{Command: `zellij_bin=$(command -v zellij) || zellij_bin="$HOME/.cargo/bin/zellij"; "$zellij_bin" kill-session ` + sshtransport.QuoteShell("ash-"+id)})
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("external kill: %+v %v", r, err)
	}
	if _, err = zellij.New(tr).Read(ctx, h, id); !errors.Is(err, shell.ErrNotFound) {
		t.Fatalf("externally killed session: %v", err)
	}
}
