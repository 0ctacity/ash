package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ash/internal/host"
	"ash/internal/policy"
	"ash/internal/shell"
)

type shellBackend struct {
	calls  int
	ids    []string
	input  string
	cwd    string
	cursor string
}

func (b *shellBackend) Name() string { return "test" }
func (b *shellBackend) Create(_ context.Context, _ host.Host, id, cwd string) error {
	b.calls++
	b.ids = append(b.ids, id)
	b.cwd = cwd
	return nil
}
func (b *shellBackend) List(context.Context, host.Host) ([]string, error) {
	b.calls++
	return append([]string(nil), b.ids...), nil
}
func (b *shellBackend) Send(_ context.Context, _ host.Host, id, input string) error {
	b.calls++
	b.input = input
	return nil
}
func (b *shellBackend) Read(_ context.Context, _ host.Host, _ string, req shell.ReadRequest) (shell.Output, error) {
	b.calls++
	b.cursor = req.Cursor
	return shell.Output{Content: "output", Cursor: "cur"}, nil
}
func (b *shellBackend) Close(context.Context, host.Host, string) error {
	b.calls++
	b.ids = nil
	return nil
}

func TestShellPolicyDeniesEveryOperationBeforeBackend(t *testing.T) {
	backend := new(shellBackend)
	s := NewShells(host.New(map[string]host.Host{"h": {Policy: policy.Policy{Read: true, Write: true}}}), backend)
	ctx := context.Background()
	id := "sh_" + strings.Repeat("a", 32)
	_, e1 := s.Create(ctx, "h", "")
	_, e2 := s.List(ctx, "h")
	e3 := s.Send(ctx, "h", id, "pwd\n")
	_, e4 := s.Read(ctx, "h", id, "")
	_, e6 := s.Wait(ctx, "h", id, shell.WaitRequest{Timeout: time.Second})
	e5 := s.Close(ctx, "h", id)
	for _, err := range []error{e1, e2, e3, e4, e5, e6} {
		if !errors.Is(err, policy.ErrPermissionDenied) {
			t.Fatalf("got %v", err)
		}
	}
	if backend.calls != 0 {
		t.Fatalf("backend called %d times", backend.calls)
	}
}
func TestShellServiceUsesBackendAcrossInstances(t *testing.T) {
	backend := new(shellBackend)
	hosts := host.New(map[string]host.Host{"h": {Policy: policy.Policy{Exec: true}}})
	ctx := context.Background()
	s := NewShells(hosts, backend)
	info, err := s.Create(ctx, "h", "~/project ' quoted")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.ID, "sh_") || len(info.ID) != 35 || info.Host != "h" || info.Backend != "test" {
		t.Fatalf("%+v", info)
	}
	if backend.cwd != "~/project ' quoted" {
		t.Fatal(backend.cwd)
	}
	s = NewShells(hosts, backend)
	list, err := s.List(ctx, "h")
	if err != nil || len(list) != 1 || list[0].ID != info.ID {
		t.Fatalf("%+v %v", list, err)
	}
	if err = s.Send(ctx, "h", info.ID, "printf literal\n"); err != nil || backend.input != "printf literal\n" {
		t.Fatalf("input %q %v", backend.input, err)
	}
	output, err := s.Read(ctx, "h", info.ID, "cursor-value")
	if err != nil || output.Content != "output" || output.Cursor != "cur" || backend.cursor != "cursor-value" {
		t.Fatalf("%+v %v", output, err)
	}
	// Liveness comes from the backend, not a record created by this service.
	backend.ids = nil
	list, err = s.List(ctx, "h")
	if err != nil || len(list) != 0 {
		t.Fatalf("%+v %v", list, err)
	}
	if err = s.Close(ctx, "h", info.ID); err != nil {
		t.Fatal(err)
	}
}

type waitBackend struct {
	shell.Backend
	outputs []shell.Output
	err     error
	i       int
	closes  int
}

func (b *waitBackend) Name() string { return "test" }
func (b *waitBackend) Read(_ context.Context, _ host.Host, _ string, _ shell.ReadRequest) (shell.Output, error) {
	if b.err != nil {
		return shell.Output{}, b.err
	}
	if b.i >= len(b.outputs) {
		return shell.Output{}, nil
	}
	out := b.outputs[b.i]
	b.i++
	return out, nil
}
func (b *waitBackend) Close(context.Context, host.Host, string) error { b.closes++; return nil }

func waitService(b *waitBackend) *ShellService {
	return NewShells(host.New(map[string]host.Host{"h": {Policy: policy.Policy{Exec: true}}}), b)
}

func TestWaitReturnsOnNewOutput(t *testing.T) {
	b := &waitBackend{outputs: []shell.Output{{Content: "hello", Cursor: "c1"}}}
	result, err := waitService(b).Wait(context.Background(), "h", "sh_"+strings.Repeat("a", 32), shell.WaitRequest{Timeout: time.Second})
	if err != nil || !result.Matched || result.Content != "hello" || result.Cursor != "c1" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestWaitMatchesAcrossChunks(t *testing.T) {
	b := &waitBackend{outputs: []shell.Output{{Content: "fo", Cursor: "c1"}, {Content: "obar", Cursor: "c2"}}}
	result, err := waitService(b).Wait(context.Background(), "h", "sh_"+strings.Repeat("a", 32), shell.WaitRequest{Literal: "foo", Timeout: time.Second})
	if err != nil || !result.Matched || result.Content != "foobar" || result.Cursor != "c2" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestWaitRegexAndTimeout(t *testing.T) {
	b := &waitBackend{outputs: []shell.Output{{Content: "err=42", Cursor: "c1"}}}
	result, err := waitService(b).Wait(context.Background(), "h", "sh_"+strings.Repeat("a", 32), shell.WaitRequest{Regex: `err=\d+`, Timeout: time.Second})
	if err != nil || !result.Matched {
		t.Fatalf("%+v %v", result, err)
	}
	// No output and no matcher: returns on timeout without closing the shell.
	empty := &waitBackend{}
	result, err = waitService(empty).Wait(context.Background(), "h", "sh_"+strings.Repeat("a", 32), shell.WaitRequest{Timeout: 40 * time.Millisecond})
	if err != nil || !result.TimedOut || result.Matched || empty.closes != 0 {
		t.Fatalf("%+v %v closes=%d", result, err, empty.closes)
	}
}

func TestWaitValidationAndClosure(t *testing.T) {
	id := "sh_" + strings.Repeat("a", 32)
	s := waitService(&waitBackend{})
	if _, err := s.Wait(context.Background(), "h", id, shell.WaitRequest{Literal: "a", Regex: "a", Timeout: time.Second}); err == nil {
		t.Fatal("accepted both matchers")
	}
	if _, err := s.Wait(context.Background(), "h", id, shell.WaitRequest{Regex: "(", Timeout: time.Second}); err == nil {
		t.Fatal("accepted invalid regex")
	}
	if _, err := s.Wait(context.Background(), "h", id, shell.WaitRequest{}); err == nil {
		t.Fatal("accepted zero timeout")
	}
	if _, err := s.Wait(context.Background(), "h", id, shell.WaitRequest{Timeout: MaxWaitTimeout + time.Second}); err == nil {
		t.Fatal("accepted oversized timeout")
	}
	closed := waitService(&waitBackend{err: shell.ErrNotFound})
	if _, err := closed.Wait(context.Background(), "h", id, shell.WaitRequest{Timeout: time.Second}); !errors.Is(err, shell.ErrNotFound) {
		t.Fatalf("closure: %v", err)
	}
}

func TestWaitCancellationDoesNotCloseShell(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &waitBackend{}
	if _, err := waitService(b).Wait(ctx, "h", "sh_"+strings.Repeat("a", 32), shell.WaitRequest{Timeout: time.Minute}); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if b.closes != 0 {
		t.Fatal("cancellation closed the shell")
	}
}

func TestShellValidationAndCancellation(t *testing.T) {
	backend := new(shellBackend)
	s := NewShells(host.New(map[string]host.Host{"h": {Policy: policy.Policy{Exec: true}}}), backend)
	ctx := context.Background()
	id := "sh_" + strings.Repeat("b", 32)
	for _, invalid := range []string{"", "personal", "sh_../../foo", id + ";true"} {
		if err := s.Send(ctx, "h", invalid, "x"); err == nil {
			t.Fatalf("accepted ID %q", invalid)
		}
	}
	for _, input := range []string{strings.Repeat("x", shell.MaxInputSize+1), "a\x00b", string([]byte{0xff})} {
		if err := s.Send(ctx, "h", id, input); err == nil {
			t.Fatal("accepted invalid input")
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Create(canceled, "h", ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "missing", ""); !errors.Is(err, host.ErrHostNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, "h", id, strings.Repeat("a", shell.MaxCursorSize+1)); err == nil {
		t.Fatal("accepted oversized cursor")
	}
	if backend.calls != 0 {
		t.Fatalf("backend called %d times", backend.calls)
	}
}
