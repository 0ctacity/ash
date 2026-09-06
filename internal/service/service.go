// Package service owns host resolution, authorization and operation deadlines.
package service

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"ash/internal/host"
	"ash/internal/transport"
)

type Service struct {
	hosts     *host.Registry
	transport transport.Transport
}

func New(hosts *host.Registry, t transport.Transport) *Service {
	return &Service{hosts: hosts, transport: t}
}

type HostInfo struct {
	Name         string   `json:"name"`
	Address      string   `json:"address"`
	User         string   `json:"user"`
	Capabilities []string `json:"capabilities"`
}

func (s *Service) Hosts() []HostInfo {
	out := make([]HostInfo, 0)
	for _, h := range s.hosts.List() {
		caps := make([]string, 0, 3)
		if h.Policy.Exec {
			caps = append(caps, "exec")
		}
		if h.Policy.Read {
			caps = append(caps, "read")
		}
		if h.Policy.Write {
			caps = append(caps, "write")
		}
		out = append(out, HostInfo{Name: h.Name, Address: h.Address, User: h.User, Capabilities: caps})
	}
	return out
}
func (s *Service) resolve(name, operation string) (host.Host, error) {
	h, err := s.hosts.Get(name)
	if err != nil {
		return h, err
	}
	if err = h.Policy.Check(operation); err != nil {
		return h, fmt.Errorf("host %q: %s: %w", name, operation, err)
	}
	return h, nil
}
func operationError(ctx context.Context, name, op string, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		err = ctx.Err()
		if err == context.DeadlineExceeded {
			err = fmt.Errorf("%w: %w", transport.ErrTimeout, err)
		}
	}
	return fmt.Errorf("host %q: %s: %w", name, op, err)
}
func (s *Service) Exec(ctx context.Context, req transport.ExecRequest) (transport.ExecResult, error) {
	h, err := s.resolve(req.Host, "exec")
	if err != nil {
		return transport.ExecResult{}, err
	}
	if req.Command == "" {
		return transport.ExecResult{}, fmt.Errorf("command must not be empty")
	}
	if req.Timeout < 0 {
		return transport.ExecResult{}, fmt.Errorf("timeout must be positive")
	}
	if req.Timeout == 0 {
		req.Timeout = transport.DefaultExecTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return transport.ExecResult{}, operationError(ctx, req.Host, "exec", err)
	}
	result, err := s.transport.Exec(ctx, h, req)
	return result, operationError(ctx, req.Host, "exec", err)
}
func (s *Service) Read(ctx context.Context, name, path string) ([]byte, error) {
	h, err := s.resolve(name, "read")
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("path must not be empty")
	}
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, operationError(ctx, name, "read", err)
	}
	data, err := s.transport.Read(ctx, h, path)
	return data, operationError(ctx, name, "read", err)
}
func (s *Service) Write(ctx context.Context, name, path string, data []byte) error {
	h, err := s.resolve(name, "write")
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("path must not be empty")
	}
	if len(data) > transport.MaxWriteSize {
		return fmt.Errorf("write exceeds %d byte limit", transport.MaxWriteSize)
	}
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return operationError(ctx, name, "write", err)
	}
	return operationError(ctx, name, "write", s.transport.Write(ctx, h, path, data))
}
func (s *Service) Stat(ctx context.Context, name, path string) (transport.FileInfo, error) {
	h, err := s.resolve(name, "stat")
	if err != nil {
		return transport.FileInfo{}, err
	}
	if path == "" {
		return transport.FileInfo{}, fmt.Errorf("path must not be empty")
	}
	ctx, cancel := context.WithTimeout(ctx, transport.DefaultFileTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return transport.FileInfo{}, operationError(ctx, name, "stat", err)
	}
	result, err := s.transport.Stat(ctx, h, path)
	return result, operationError(ctx, name, "stat", err)
}

// Text rejects binary data rather than silently replacing invalid UTF-8 in JSON.
func Text(data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", fmt.Errorf("file is not UTF-8 text; binary MCP reads are unsupported")
	}
	return string(data), nil
}

// TimeoutMillis validates a wire timeout before converting it to a duration.
func TimeoutMillis(ms int64) (time.Duration, error) {
	if ms < 0 || ms > int64((1<<63-1)/time.Millisecond) {
		return 0, fmt.Errorf("timeout_ms is out of range")
	}
	return time.Duration(ms) * time.Millisecond, nil
}
