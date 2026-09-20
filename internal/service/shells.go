package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"ash/internal/audit"
	"ash/internal/host"
	"ash/internal/shell"
	"ash/internal/transport"
)

// ShellService authorizes persistent-shell operations independently of one-shot exec.
// It keeps no session state; the backend is the authority on remote liveness.
type ShellService struct {
	hosts   *host.Registry
	backend shell.Backend
	audit   *audit.Recorder
}

func NewShells(hosts *host.Registry, backend shell.Backend) *ShellService {
	return &ShellService{hosts: hosts, backend: backend}
}

// WithAudit attaches one audit recorder to the shell service.
func (s *ShellService) WithAudit(recorder *audit.Recorder) *ShellService {
	s.audit = recorder
	return s
}

func (s *ShellService) finish(operation, name string, started time.Time, err error) {
	if s.audit == nil {
		return
	}
	record := audit.Record{Operation: operation, Host: name, Decision: audit.Allowed, Result: audit.ResultOK, DurationMS: time.Since(started).Milliseconds()}
	if err != nil {
		record.Result = audit.ResultError
	}
	s.audit.Record(record)
}

func (s *ShellService) resolve(ctx context.Context, name string) (host.Host, error) {
	h, err := s.hosts.Get(name)
	if err != nil {
		return h, err
	}
	if err = h.Policy.Check("exec"); err != nil {
		err = fmt.Errorf("host %q: shell: %w", name, err)
		s.audit.Record(audit.Record{Operation: "shell", Host: name, Decision: audit.Denied, Result: audit.ResultError})
		return h, err
	}
	if err = ctx.Err(); err != nil {
		return h, operationError(ctx, name, "shell", err)
	}
	return h, nil
}

func (s *ShellService) info(name, id string) shell.Info {
	return shell.Info{ID: id, Host: name, Backend: s.backend.Name()}
}

func (s *ShellService) Create(ctx context.Context, name, cwd string) (info shell.Info, err error) {
	started := time.Now()
	defer func() { s.finish("shell create", name, started, err) }()
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

func (s *ShellService) List(ctx context.Context, name string) (out []shell.Info, err error) {
	started := time.Now()
	defer func() { s.finish("shell list", name, started, err) }()
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
	out = make([]shell.Info, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.info(name, id))
	}
	return out, nil
}

func (s *ShellService) Send(ctx context.Context, name, id, input string) (err error) {
	started := time.Now()
	defer func() { s.finish("shell send", name, started, err) }()
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

func (s *ShellService) Read(ctx context.Context, name, id string) (out shell.Output, err error) {
	started := time.Now()
	defer func() { s.finish("shell read", name, started, err) }()
	h, err := s.resolve(ctx, name)
	if err != nil {
		return shell.Output{}, err
	}
	if err = shell.ValidateID(id); err != nil {
		return shell.Output{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	out, err = s.backend.Read(ctx, h, id)
	// Backend errors carry no bound fields here; report the resolved error.
	if err != nil {
		return out, operationError(ctx, name, "shell read", err)
	}
	return out, nil
}

func (s *ShellService) Close(ctx context.Context, name, id string) (err error) {
	started := time.Now()
	defer func() { s.finish("shell close", name, started, err) }()
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
