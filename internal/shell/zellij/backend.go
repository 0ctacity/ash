// Package zellij controls named, detached Zellij sessions over short SSH calls.
package zellij

import (
	"ash/internal/host"
	"ash/internal/shell"
	"ash/internal/transport"
	sshtransport "ash/internal/transport/ssh"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Backend struct{ transport transport.Transport }

func New(t transport.Transport) *Backend { return &Backend{transport: t} }
func (*Backend) Name() string            { return "zellij" }

// Resolve the ordinary PATH first, then Cargo's conventional installation path.
// Every invocation verifies the backend version and ignores personal config.
const prelude = `unset ZELLIJ ZELLIJ_SESSION_NAME
zellij_bin=$(command -v zellij) || zellij_bin="$HOME/.cargo/bin/zellij"
[ -x "$zellij_bin" ] || { printf 'Zellij 0.44 or newer is required\n' >&2; exit 127; }
version=$("$zellij_bin" --version) || exit 127
version=${version#zellij }
major=${version%%.*}
rest=${version#*.}
minor=${rest%%.*}
case "$major:$minor" in *[!0-9:]*|:*|*:) exit 127;; esac
if [ "$major" -eq 0 ] && [ "$minor" -lt 44 ]; then printf 'Zellij 0.44 or newer is required\n' >&2; exit 127; fi
`
const zellij = `"$zellij_bin" --config /dev/null `

func (b *Backend) run(ctx context.Context, h host.Host, command, cwd string) (transport.ExecResult, error) {
	result, err := b.transport.Exec(ctx, h, transport.ExecRequest{Command: prelude + command, Cwd: cwd, Timeout: 30 * time.Second})
	if err != nil {
		return result, err
	}
	if result.ExitCode == 127 {
		return result, fmt.Errorf("%w: Zellij 0.44 or newer is required", shell.ErrUnavailable)
	}
	if result.ExitCode != 0 {
		return result, &commandError{code: result.ExitCode, message: strings.TrimSpace(result.Stderr)}
	}
	return result, nil
}
func (b *Backend) List(ctx context.Context, h host.Host) ([]string, error) {
	r, err := b.run(ctx, h, zellij+"list-sessions --no-formatting", "")
	if err != nil {
		var commandErr *commandError
		if errors.As(err, &commandErr) && r.ExitCode == 1 && strings.Contains(r.Stderr+r.Stdout, "No active zellij sessions found") {
			return []string{}, nil
		}
		return nil, err
	}
	if r.StdoutTruncated {
		return nil, fmt.Errorf("zellij session list exceeds output limit")
	}
	ids := []string{}
	for _, line := range strings.Split(r.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.Contains(line, "(EXITED") {
			continue
		}
		name := fields[0]
		if !strings.HasPrefix(name, "ash-") {
			continue
		}
		id := strings.TrimPrefix(name, "ash-")
		if shell.ValidateID(id) == nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
func (b *Backend) live(ctx context.Context, h host.Host, id string) error {
	if err := shell.ValidateID(id); err != nil {
		return err
	}
	ids, err := b.List(ctx, h)
	if err != nil {
		return err
	}
	for _, found := range ids {
		if found == id {
			return nil
		}
	}
	return shell.ErrNotFound
}
func (b *Backend) Create(ctx context.Context, h host.Host, id, cwd string) error {
	if err := shell.ValidateID(id); err != nil {
		return err
	}
	ids, err := b.List(ctx, h)
	if err != nil {
		return err
	}
	for _, found := range ids {
		if found == id {
			return fmt.Errorf("shell already exists")
		}
	}
	// Keep the per-session config while the daemon runs: it reloads this path.
	// The deterministic remote path requires no local session metadata.
	command := `umask 077
ash_dir="$HOME/.cache/ash/shells/` + id + `"
mkdir -p -- "$HOME/.cache/ash/shells" || exit 1
mkdir -- "$ash_dir" || exit 1
ash_started=0
trap 'if [ "$ash_started" = 0 ]; then rm -f -- "$ash_dir/config.kdl"; rmdir -- "$ash_dir"; fi' EXIT HUP INT TERM
printf '%s' ` + sshtransport.QuoteShell("load_plugins {}\nshow_startup_tips false\nshow_release_notes false\nsession_serialization false\nweb_server false\nweb_sharing \"disabled\"\n") + ` > "$ash_dir/config.kdl" || exit 1
"$zellij_bin" --config "$ash_dir/config.kdl" --layout-string 'layout { pane; }' attach --create-background ` + sshtransport.QuoteShell("ash-"+id) + " || exit 1\nash_started=1"
	if _, err = b.run(ctx, h, command, cwd); err != nil {
		return err
	}
	return b.live(ctx, h, id)
}
func (b *Backend) Send(ctx context.Context, h host.Host, id, input string) error {
	if err := shell.ValidateID(id); err != nil {
		return err
	}
	if len(input) > shell.MaxInputSize {
		return fmt.Errorf("shell input exceeds %d bytes", shell.MaxInputSize)
	}
	if strings.ContainsRune(input, 0) {
		return fmt.Errorf("shell input cannot contain NUL")
	}
	pane, err := b.terminal(ctx, h, id)
	if err != nil {
		return err
	}
	_, err = b.run(ctx, h, sessionCommand(id)+"--session "+sshtransport.QuoteShell("ash-"+id)+" action write-chars --pane-id "+pane+" -- "+sshtransport.QuoteShell(input), "")
	return err
}
func (b *Backend) Read(ctx context.Context, h host.Host, id string) (shell.Output, error) {
	pane, err := b.terminal(ctx, h, id)
	if err != nil {
		return shell.Output{}, err
	}
	r, err := b.run(ctx, h, sessionCommand(id)+"--session "+sshtransport.QuoteShell("ash-"+id)+" action dump-screen --full --pane-id "+pane, "")
	return shell.Output{Content: r.Stdout, Truncated: r.StdoutTruncated}, err
}
func (b *Backend) Close(ctx context.Context, h host.Host, id string) error {
	if err := shell.ValidateID(id); err != nil {
		return err
	}
	// Query real state even for an already-ended shell; never trust local metadata.
	if _, err := b.List(ctx, h); err != nil {
		return err
	}
	r, err := b.run(ctx, h, zellij+"delete-session --force "+sshtransport.QuoteShell("ash-"+id), "")
	if err != nil {
		var commandErr *commandError
		if !errors.As(err, &commandErr) || r.ExitCode != 2 || strings.TrimSpace(r.Stderr+r.Stdout) != `Session: "ash-`+id+`" not found.` {
			return err
		}
	}
	_, err = b.run(ctx, h, `rm -f -- "$HOME/.cache/ash/shells/`+id+`/config.kdl" && { [ ! -d "$HOME/.cache/ash/shells/`+id+`" ] || rmdir -- "$HOME/.cache/ash/shells/`+id+`"; }`, "")
	return err
}

type commandError struct {
	code    int
	message string
}

func (e *commandError) Error() string {
	return fmt.Sprintf("zellij command failed (exit %d): %s", e.code, e.message)
}

func (b *Backend) terminal(ctx context.Context, h host.Host, id string) (string, error) {
	if err := b.live(ctx, h, id); err != nil {
		return "", err
	}
	r, err := b.run(ctx, h, sessionCommand(id)+"--session "+sshtransport.QuoteShell("ash-"+id)+" action list-panes --json", "")
	if err != nil {
		return "", err
	}
	var panes []struct {
		ID       int  `json:"id"`
		IsPlugin bool `json:"is_plugin"`
		Exited   bool `json:"exited"`
	}
	if err = json.Unmarshal([]byte(r.Stdout), &panes); err != nil {
		return "", fmt.Errorf("parse zellij panes: %w", err)
	}
	found := ""
	for _, pane := range panes {
		if pane.IsPlugin || pane.Exited {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("ASH shell must have exactly one terminal pane")
		}
		if pane.ID < 0 {
			return "", fmt.Errorf("invalid zellij pane ID")
		}
		found = fmt.Sprintf("terminal_%d", pane.ID)
	}
	if found == "" {
		return "", shell.ErrNotFound
	}
	return found, nil
}

func sessionCommand(id string) string {
	return `"$zellij_bin" --config "$HOME/.cache/ash/shells/` + id + `/config.kdl" `
}
