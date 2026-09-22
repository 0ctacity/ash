package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"ash/internal/host"
	"ash/internal/shell"
	"ash/internal/transport"
)

const (
	// MaxWaitTimeout caps one wait request.
	MaxWaitTimeout = 5 * time.Minute
	minWaitPoll    = 100 * time.Millisecond
	maxWaitPoll    = time.Second
)

// waitSleep is a seam so tests can drive polling deterministically.
var waitSleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ShellService authorizes persistent-shell operations independently of one-shot
// exec. It keeps no session state; the selected backend is the authority on
// remote liveness. Backend selection is per host and never exposed as
// backend-specific operations to CLI or MCP.
type ShellService struct {
	hosts          *host.Registry
	backends       map[string]shell.Backend
	defaultBackend string
}

func NewShells(hosts *host.Registry, backend shell.Backend) *ShellService {
	return NewShellsWithBackends(hosts, backend)
}

// NewShellsWithBackends registers one or more shell backends. The first
// non-nil backend is the default for hosts that do not name one.
func NewShellsWithBackends(hosts *host.Registry, backends ...shell.Backend) *ShellService {
	registry := make(map[string]shell.Backend, len(backends))
	defaultBackend := ""
	for _, backend := range backends {
		if backend == nil {
			continue
		}
		registry[backend.Name()] = backend
		if defaultBackend == "" {
			defaultBackend = backend.Name()
		}
	}
	return &ShellService{hosts: hosts, backends: registry, defaultBackend: defaultBackend}
}

func (s *ShellService) backendFor(h host.Host) (shell.Backend, error) {
	name := h.ShellBackend
	if name == "" {
		name = s.defaultBackend
	}
	backend, ok := s.backends[name]
	if !ok {
		return nil, fmt.Errorf("host %q: shell backend %q is not available", h.Name, name)
	}
	return backend, nil
}

func (s *ShellService) resolve(ctx context.Context, name string) (host.Host, error) {
	h, err := s.hosts.Get(name)
	if err != nil {
		return h, err
	}
	if err = h.Policy.Check("exec"); err != nil {
		return h, fmt.Errorf("host %q: shell: %w", name, err)
	}
	if err = ctx.Err(); err != nil {
		return h, operationError(ctx, name, "shell", err)
	}
	return h, nil
}

func (s *ShellService) info(backend shell.Backend, name, id string) shell.Info {
	return shell.Info{ID: id, Host: name, Backend: backend.Name()}
}

func (s *ShellService) Create(ctx context.Context, name, cwd string) (shell.Info, error) {
	h, err := s.resolve(ctx, name)
	if err != nil {
		return shell.Info{}, err
	}
	backend, err := s.backendFor(h)
	if err != nil {
		return shell.Info{}, err
	}
	if strings.ContainsRune(cwd, 0) || !utf8.ValidString(cwd) {
		return shell.Info{}, fmt.Errorf("cwd must be UTF-8 without NUL")
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return shell.Info{}, fmt.Errorf("generate shell ID: %w", err)
	}
	id := "sh_" + hex.EncodeToString(random[:])
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	if err = backend.Create(ctx, h, id, cwd); err != nil {
		return shell.Info{}, operationError(ctx, name, "shell create", err)
	}
	return s.info(backend, name, id), nil
}

func (s *ShellService) List(ctx context.Context, name string) ([]shell.Info, error) {
	h, err := s.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	backend, err := s.backendFor(h)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	ids, err := backend.List(ctx, h)
	if err != nil {
		return nil, operationError(ctx, name, "shell list", err)
	}
	sort.Strings(ids)
	out := make([]shell.Info, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.info(backend, name, id))
	}
	return out, nil
}

func (s *ShellService) Send(ctx context.Context, name, id, input string) error {
	h, err := s.resolve(ctx, name)
	if err != nil {
		return err
	}
	backend, err := s.backendFor(h)
	if err != nil {
		return err
	}
	if err = shell.ValidateID(id); err != nil {
		return err
	}
	if len(input) > shell.MaxInputSize {
		return fmt.Errorf("shell input exceeds %d byte limit", shell.MaxInputSize)
	}
	if !utf8.ValidString(input) || strings.ContainsRune(input, 0) {
		return fmt.Errorf("shell input must be UTF-8 without NUL")
	}
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	return operationError(ctx, name, "shell send", backend.Send(ctx, h, id, input))
}

func (s *ShellService) Read(ctx context.Context, name, id, cursor string) (shell.Output, error) {
	h, err := s.resolve(ctx, name)
	if err != nil {
		return shell.Output{}, err
	}
	backend, err := s.backendFor(h)
	if err != nil {
		return shell.Output{}, err
	}
	if err = shell.ValidateID(id); err != nil {
		return shell.Output{}, err
	}
	if len(cursor) > shell.MaxCursorSize {
		return shell.Output{}, fmt.Errorf("shell cursor exceeds %d byte limit", shell.MaxCursorSize)
	}
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	output, err := backend.Read(ctx, h, id, shell.ReadRequest{Cursor: cursor})
	return output, operationError(ctx, name, "shell read", err)
}

// Wait polls for new output or a matcher without closing the shell on timeout.
// It accumulates only bounded new output and matches across chunk boundaries.
func (s *ShellService) Wait(ctx context.Context, name, id string, req shell.WaitRequest) (shell.WaitResult, error) {
	h, err := s.resolve(ctx, name)
	if err != nil {
		return shell.WaitResult{}, err
	}
	if err = shell.ValidateID(id); err != nil {
		return shell.WaitResult{}, err
	}
	backend, err := s.backendFor(h)
	if err != nil {
		return shell.WaitResult{}, err
	}
	if len(req.Cursor) > shell.MaxCursorSize {
		return shell.WaitResult{}, fmt.Errorf("shell cursor exceeds %d byte limit", shell.MaxCursorSize)
	}
	if req.Literal != "" && req.Regex != "" {
		return shell.WaitResult{}, fmt.Errorf("provide only one of literal or regex")
	}
	if req.Timeout <= 0 || req.Timeout > MaxWaitTimeout {
		return shell.WaitResult{}, fmt.Errorf("timeout must be positive and at most %s", MaxWaitTimeout)
	}
	var matcher *regexp.Regexp
	if req.Regex != "" {
		matcher, err = regexp.Compile(req.Regex)
		if err != nil {
			return shell.WaitResult{}, fmt.Errorf("invalid regex: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	result := shell.WaitResult{Cursor: req.Cursor}
	accumulated := make([]byte, 0, 4096)
	interval := minWaitPoll
	for {
		output, readErr := backend.Read(ctx, h, id, shell.ReadRequest{Cursor: result.Cursor})
		if readErr != nil {
			return result, operationError(ctx, name, "shell wait", readErr)
		}
		result.Cursor = output.Cursor
		result.Truncated = result.Truncated || output.Truncated
		result.Resync = result.Resync || output.Resync
		changed := output.Content != ""
		if changed {
			remaining := shell.MaxWaitOutputSize - len(accumulated)
			if remaining <= 0 {
				result.Truncated = true
			} else if len(output.Content) > remaining {
				accumulated = append(accumulated, output.Content[:remaining]...)
				result.Truncated = true
			} else {
				accumulated = append(accumulated, output.Content...)
			}
		}
		if matchWait(req, matcher, accumulated) {
			result.Matched = true
			result.Content = string(accumulated)
			return result, nil
		}
		if result.Truncated {
			result.Content = string(accumulated)
			return result, nil
		}
		if changed {
			interval = minWaitPoll
		} else if interval < maxWaitPoll {
			interval *= 2
			if interval > maxWaitPoll {
				interval = maxWaitPoll
			}
		}
		if err := waitSleep(ctx, interval); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				result.TimedOut = true
				result.Content = string(accumulated)
				return result, nil
			}
			return result, err
		}
	}
}

// matchWait applies the optional matcher. With no matcher, any new output is a match.
func matchWait(req shell.WaitRequest, matcher *regexp.Regexp, accumulated []byte) bool {
	switch {
	case req.Literal == "" && req.Regex == "":
		return len(accumulated) > 0
	case req.Literal != "":
		return strings.Contains(string(accumulated), req.Literal)
	default:
		return matcher.Match(accumulated)
	}
}

func (s *ShellService) Close(ctx context.Context, name, id string) error {
	h, err := s.resolve(ctx, name)
	if err != nil {
		return err
	}
	backend, err := s.backendFor(h)
	if err != nil {
		return err
	}
	if err = shell.ValidateID(id); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	return operationError(ctx, name, "shell close", backend.Close(ctx, h, id))
}
