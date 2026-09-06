package service

import (
	"context"
	"errors"
	"testing"

	"ash/internal/host"
	"ash/internal/policy"
	"ash/internal/transport"
)

type fakeTransport struct{ calls int }

func (f *fakeTransport) Exec(context.Context, host.Host, transport.ExecRequest) (transport.ExecResult, error) {
	f.calls++
	return transport.ExecResult{ExitCode: 7}, nil
}
func (f *fakeTransport) Read(context.Context, host.Host, string) ([]byte, error) {
	f.calls++
	return []byte("hello"), nil
}
func (f *fakeTransport) Write(context.Context, host.Host, string, []byte) error {
	f.calls++
	return nil
}
func (f *fakeTransport) Stat(context.Context, host.Host, string) (transport.FileInfo, error) {
	f.calls++
	return transport.FileInfo{}, nil
}
func TestDeniedOperationsNeverReachTransport(t *testing.T) {
	f := new(fakeTransport)
	s := New(host.New(map[string]host.Host{"denied": {}}), f)
	ctx := context.Background()
	_, e1 := s.Exec(ctx, transport.ExecRequest{Host: "denied", Command: "true"})
	_, e2 := s.Read(ctx, "denied", "/x")
	e3 := s.Write(ctx, "denied", "/x", nil)
	_, e4 := s.Stat(ctx, "denied", "/x")
	for _, err := range []error{e1, e2, e3, e4} {
		if !errors.Is(err, policy.ErrPermissionDenied) {
			t.Fatalf("got %v", err)
		}
	}
	if f.calls != 0 {
		t.Fatalf("transport called %d times", f.calls)
	}
	_, err := s.Exec(ctx, transport.ExecRequest{Host: "missing", Command: "true"})
	if !errors.Is(err, host.ErrHostNotFound) {
		t.Fatal(err)
	}
}
func TestExecNonzeroIsResultAndHostsHideCredentials(t *testing.T) {
	f := new(fakeTransport)
	s := New(host.New(map[string]host.Host{"allowed": {Identity: "secret", Policy: policy.Policy{Exec: true}}}), f)
	r, err := s.Exec(context.Background(), transport.ExecRequest{Host: "allowed", Command: "false"})
	if err != nil || r.ExitCode != 7 {
		t.Fatalf("%+v %v", r, err)
	}
	hs := s.Hosts()
	if len(hs) != 1 || len(hs[0].Capabilities) != 1 || hs[0].Capabilities[0] != "exec" {
		t.Fatalf("%+v", hs)
	}
}

func TestCanceledAndOversizedRequestsDoNotReachTransport(t *testing.T) {
	f := new(fakeTransport)
	s := New(host.New(map[string]host.Host{"h": {Policy: policy.Policy{Exec: true, Read: true, Write: true}}}), f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e1 := s.Exec(ctx, transport.ExecRequest{Host: "h", Command: "true"})
	_, e2 := s.Read(ctx, "h", "/x")
	e3 := s.Write(ctx, "h", "/x", nil)
	_, e4 := s.Stat(ctx, "h", "/x")
	for _, err := range []error{e1, e2, e3, e4} {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	}
	if err := s.Write(context.Background(), "h", "/x", make([]byte, transport.MaxWriteSize+1)); err == nil {
		t.Fatal("oversized write accepted")
	}
	if f.calls != 0 {
		t.Fatalf("transport called %d times", f.calls)
	}
}
func TestTextAndWireTimeoutValidation(t *testing.T) {
	if _, err := Text([]byte{0xff}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	for _, ms := range []int64{-1, 1<<63 - 1} {
		if _, err := TimeoutMillis(ms); err == nil {
			t.Fatalf("accepted %d", ms)
		}
	}
}
