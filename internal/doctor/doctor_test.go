package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ash/internal/host"
	"ash/internal/policy"
	"ash/internal/transport"
)

type fakeTransport struct {
	transport.Transport
	err      error
	results  map[string]transport.ExecResult
	commands []string
}

func (f *fakeTransport) Exec(_ context.Context, _ host.Host, req transport.ExecRequest) (transport.ExecResult, error) {
	f.commands = append(f.commands, req.Command)
	if f.err != nil {
		return transport.ExecResult{}, f.err
	}
	if r, ok := f.results[req.Command]; ok {
		return r, nil
	}
	return transport.ExecResult{}, nil
}

func testDoctor(t *testing.T, f *fakeTransport, pol policy.Policy) *Doctor {
	t.Helper()
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(known, []byte("host ssh-ed25519 AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return New(host.New(map[string]host.Host{"h": {Address: "example", Port: 22, User: "u", Policy: pol}}), f, known)
}

func find(checks []Check) (Check, bool) {
	for _, check := range checks {
		if check.Name == "trust" || check.Name == "auth" || check.Name == "network" || check.Name == "shell" || check.Name == "cache" || check.Name == "zellij" {
			return check, true
		}
	}
	return Check{}, false
}

func TestRemoteSuccess(t *testing.T) {
	f := &fakeTransport{results: map[string]transport.ExecResult{
		`command -v zellij >/dev/null 2>&1 && zellij --version`: {Stdout: "zellij 0.44.0\n"},
	}}
	d := testDoctor(t, f, policy.Policy{Exec: true})
	report := d.Run(context.Background(), "h")
	if report.Failed() {
		t.Fatalf("unexpected failure: %+v", report)
	}
	names := map[string]Status{}
	for _, check := range report.Checks {
		names[check.Name] = check.Status
	}
	for _, name := range []string{"config", "known_hosts", "host", "policy", "trust", "auth", "shell", "cache", "zellij"} {
		if names[name] != StatusOK {
			t.Fatalf("check %s = %s (%+v)", name, names[name], report.Checks)
		}
	}
}

func TestMissingZellijIsWarningNotFailure(t *testing.T) {
	f := &fakeTransport{results: map[string]transport.ExecResult{
		`command -v zellij >/dev/null 2>&1 && zellij --version`: {ExitCode: 1},
	}}
	report := testDoctor(t, f, policy.Policy{Exec: true}).Run(context.Background(), "h")
	if report.Failed() {
		t.Fatalf("missing zellij should warn: %+v", report.Checks)
	}
	for _, check := range report.Checks {
		if check.Name == "zellij" && check.Status != StatusWarn {
			t.Fatalf("zellij = %s", check.Status)
		}
	}
}

func TestTrustAndAuthFailuresAreDistinguished(t *testing.T) {
	trust := testDoctor(t, &fakeTransport{err: fmt.Errorf("%w: not trusted", transport.ErrHostKey)}, policy.Policy{Exec: true})
	report := trust.Run(context.Background(), "h")
	if !report.Failed() {
		t.Fatal("expected failure")
	}
	check, ok := find(report.Checks)
	if !ok || check.Name != "trust" || check.Status != StatusFail {
		t.Fatalf("%+v", report.Checks)
	}
	auth := testDoctor(t, &fakeTransport{err: fmt.Errorf("%w: bad key", transport.ErrAuthentication)}, policy.Policy{Exec: true})
	report = auth.Run(context.Background(), "h")
	check, ok = find(report.Checks)
	if !ok || check.Name != "auth" || check.Status != StatusFail {
		t.Fatalf("%+v", report.Checks)
	}
}

func TestRemoteChecksSkipWithoutExec(t *testing.T) {
	f := &fakeTransport{}
	report := testDoctor(t, f, policy.Policy{Read: true}).Run(context.Background(), "h")
	for _, check := range report.Checks {
		if check.Name == "shell" && check.Status != StatusSkip {
			t.Fatalf("shell = %s", check.Status)
		}
	}
	if len(f.commands) != 1 {
		t.Fatalf("expected only the connectivity probe, got %v", f.commands)
	}
}

func TestUnknownHostAndLocalChecks(t *testing.T) {
	f := &fakeTransport{}
	d := testDoctor(t, f, policy.Policy{Exec: true})
	report := d.Run(context.Background(), "missing")
	if !report.Failed() {
		t.Fatalf("expected failure: %+v", report.Checks)
	}
	if len(f.commands) != 0 {
		t.Fatalf("unknown host should not connect: %v", f.commands)
	}
	local := d.Local()
	if local.Failed() {
		t.Fatalf("local checks failed: %+v", local.Checks)
	}
	missing := New(host.New(nil), f, filepath.Join(t.TempDir(), "absent"))
	for _, check := range missing.Local().Checks {
		if check.Name == "known_hosts" && check.Status != StatusWarn {
			t.Fatalf("known_hosts = %s", check.Status)
		}
	}
}

func TestDiagnosticsDoNotEchoIdentityPaths(t *testing.T) {
	known := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(known, []byte("x\n"), 0o600)
	d := New(host.New(map[string]host.Host{"h": {Identity: "/secret/id_rsa", Policy: policy.Policy{Exec: true}}}), &fakeTransport{err: fmt.Errorf("%w: read identity /secret/id_rsa", transport.ErrAuthentication)}, known)
	report := d.Run(context.Background(), "h")
	for _, check := range report.Checks {
		if strings.Contains(check.Message+check.Hint, "/secret") {
			t.Fatalf("diagnostic leaked identity path: %+v", check)
		}
	}
	if report.Host != "h" {
		t.Fatalf("%+v", report)
	}
}
