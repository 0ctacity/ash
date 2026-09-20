// Package tmux controls named, detached tmux sessions over short SSH calls.
//
// tmux is isolated behind the -L ash server socket so ASH only ever lists and
// controls its own ash-<id> sessions, never the user's personal sessions.
package tmux

import (
	"ash/internal/host"
	"ash/internal/shell"
	"ash/internal/transport"
	sshtransport "ash/internal/transport/ssh"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Backend struct{ transport transport.Transport }

func New(t transport.Transport) *Backend { return &Backend{transport: t} }
func (*Backend) Name() string            { return "tmux" }

// prelude resolves tmux and fails with a backend-unavailable error when absent.
const prelude = `unset TMUX
tmux_bin=$(command -v tmux) || { printf 'tmux is required for the tmux shell backend\n' >&2; exit 127; }
`
const tmux = `"$tmux_bin" -L ash `

func (b *Backend) run(ctx context.Context, h host.Host, command, cwd string) (transport.ExecResult, error) {
	result, err := b.transport.Exec(ctx, h, transport.ExecRequest{Command: prelude + command, Cwd: cwd, Timeout: 30 * time.Second})
	if err != nil {
		return result, err
	}
	if result.ExitCode == 127 {
		return result, fmt.Errorf("%w: tmux is required for the tmux shell backend", shell.ErrUnavailable)
	}
	if result.ExitCode != 0 {
		return result, &commandError{code: result.ExitCode, message: strings.TrimSpace(result.Stderr)}
	}
	return result, nil
}

func sessionName(id string) string { return "ash-" + id }

// List returns live ASH-owned tmux sessions, tolerant of a missing server.
func (b *Backend) List(ctx context.Context, h host.Host) ([]string, error) {
	r, err := b.run(ctx, h, tmux+`list-sessions -F '#{session_name}'`, "")
	if err != nil {
		var commandErr *commandError
		if errors.As(err, &commandErr) && r.ExitCode == 1 && noServer(r.Stderr+r.Stdout) {
			return []string{}, nil
		}
		return nil, err
	}
	if r.StdoutTruncated {
		return nil, fmt.Errorf("tmux session list exceeds output limit")
	}
	ids := []string{}
	for _, line := range strings.Split(r.Stdout, "\n") {
		name := strings.TrimSpace(line)
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

func noServer(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "no server running") || strings.Contains(lower, "no sessions") || strings.Contains(lower, "error connecting to")
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
	command := tmux + "new-session -d -s " + sshtransport.QuoteShell(sessionName(id)) + " -x 200 -y 50"
	if cwd != "" {
		command += " -c " + expandCwd(cwd)
	}
	if _, err := b.run(ctx, h, command, ""); err != nil {
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
	if err := b.live(ctx, h, id); err != nil {
		return err
	}
	_, err := b.run(ctx, h, tmux+"send-keys -t "+sshtransport.QuoteShell(sessionName(id))+" -l -- "+sshtransport.QuoteShell(input), "")
	return err
}

func (b *Backend) Read(ctx context.Context, h host.Host, id string) (shell.Output, error) {
	if err := b.live(ctx, h, id); err != nil {
		return shell.Output{}, err
	}
	r, err := b.run(ctx, h, tmux+"capture-pane -p -t "+sshtransport.QuoteShell(sessionName(id))+" -S -", "")
	return shell.Output{Content: r.Stdout, Truncated: r.StdoutTruncated}, err
}

func (b *Backend) Close(ctx context.Context, h host.Host, id string) error {
	if err := shell.ValidateID(id); err != nil {
		return err
	}
	// Query real state first; never trust local metadata. A missing server means
	// there is nothing to close.
	if _, err := b.List(ctx, h); err != nil {
		return err
	}
	r, err := b.run(ctx, h, tmux+"kill-session -t "+sshtransport.QuoteShell(sessionName(id)), "")
	if err != nil {
		var commandErr *commandError
		if !errors.As(err, &commandErr) || !sessionGone(r.Stderr+r.Stdout) {
			return err
		}
	}
	return nil
}

func sessionGone(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "can't find session") || strings.Contains(lower, "session not found") || strings.Contains(lower, "no server running")
}

// expandCwd supports ~ by expanding it in the remote shell, matching exec.
func expandCwd(cwd string) string {
	switch {
	case cwd == "~":
		return `"$HOME"`
	case strings.HasPrefix(cwd, "~/"):
		return `"$HOME"/` + sshtransport.QuoteShell(cwd[2:])
	default:
		return sshtransport.QuoteShell(cwd)
	}
}

type commandError struct {
	code    int
	message string
}

func (e *commandError) Error() string {
	return fmt.Sprintf("tmux command failed (exit %d): %s", e.code, e.message)
}
