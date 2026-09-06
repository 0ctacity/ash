package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"ash/internal/host"
	"ash/internal/shell"
	"ash/internal/transport"
)

// ShellService authorizes persistent-shell operations independently of one-shot exec.
// It keeps no session state; the backend is the authority on remote liveness.
type ShellService struct {
	hosts   *host.Registry
	backend shell.Backend
}

func NewShells(hosts *host.Registry, backend shell.Backend) *ShellService {
	return &ShellService{hosts: hosts, backend: backend}
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

func (s *ShellService) info(name, id string) shell.Info {
	return shell.Info{ID: id, Host: name, Backend: s.backend.Name()}
}

func (s *ShellService) Create(ctx context.Context, name, cwd string) (shell.Info, error) {
	h, err := s.resolve(ctx, name)
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
	if err = s.backend.Create(ctx, h, id, cwd); err != nil {
		return shell.Info{}, operationError(ctx, name, "shell create", err)
	}
	return s.info(name, id), nil
}

func (s *ShellService) List(ctx context.Context, name string) ([]shell.Info, error) {
	h, err := s.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	ids, err := s.backend.List(ctx, h)
	if err != nil {
		return nil, operationError(ctx, name, "shell list", err)
	}
	sort.Strings(ids)
	out := make([]shell.Info, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.info(name, id))
	}
	return out, nil
}

func (s *ShellService) Send(ctx context.Context, name, id, input string) error {
	h, err := s.resolve(ctx, name)
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
	return operationError(ctx, name, "shell send", s.backend.Send(ctx, h, id, input))
}

func (s *ShellService) Read(ctx context.Context, name, id string) (shell.Output, error) {
	h, err := s.resolve(ctx, name)
	if err != nil {
		return shell.Output{}, err
	}
	if err = shell.ValidateID(id); err != nil {
		return shell.Output{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	output, err := s.backend.Read(ctx, h, id)
	return output, operationError(ctx, name, "shell read", err)
}

func (s *ShellService) Close(ctx context.Context, name, id string) error {
	h, err := s.resolve(ctx, name)
	if err != nil {
		return err
	}
	if err = shell.ValidateID(id); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	return operationError(ctx, name, "shell close", s.backend.Close(ctx, h, id))
}
