package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ash/internal/host"
	"ash/internal/policy"
	"ash/internal/shell"
)

type shellBackend struct {
	calls int
	ids   []string
	input string
	cwd   string
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
func (b *shellBackend) Read(context.Context, host.Host, string) (shell.Output, error) {
	b.calls++
	return shell.Output{Content: "output"}, nil
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
	_, e4 := s.Read(ctx, "h", id)
	e5 := s.Close(ctx, "h", id)
	for _, err := range []error{e1, e2, e3, e4, e5} {
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
	output, err := s.Read(ctx, "h", info.ID)
	if err != nil || output.Content != "output" {
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
	if backend.calls != 0 {
		t.Fatalf("backend called %d times", backend.calls)
	}
}
