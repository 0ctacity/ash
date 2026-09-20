package policy

import (
	"errors"
	"testing"
	"time"
)

func TestDefaultsPreserveBroadBehavior(t *testing.T) {
	p := Policy{Exec: true, Read: true, Write: true}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := p.Check("exec"); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckShell(); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckExecutable("/any/binary"); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckRoots("/etc/passwd", false); err != nil {
		t.Fatal(err)
	}
	if p.MaxTimeout() != 0 {
		t.Fatal("unexpected default timeout")
	}
}

func TestValidateRootsAndBounds(t *testing.T) {
	for name, p := range map[string]Policy{
		"relative root": {ReadRoots: []string{"relative"}},
		"unclean root":  {WriteRoots: []string{"/a/../b"}},
		"nul root":      {CwdRoots: []string{"/a\x00"}},
		"timeout bound": {MaxTimeoutSeconds: HardMaxTimeoutSeconds + 1},
		"input bound":   {MaxInputBytes: HardMaxInputBytes + 1},
		"output bound":  {MaxOutputBytes: HardMaxOutputBytes + 1},
		"empty command": {AllowedCommands: []string{""}},
	} {
		if err := p.Validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err := (Policy{ReadRoots: []string{"/var/log"}, MaxTimeoutSeconds: 30}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCommandConstraintRejectsShellCode(t *testing.T) {
	p := Policy{Exec: true, AllowedCommands: []string{"ls", "cat"}}
	if err := p.CheckShell(); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("shell code accepted: %v", err)
	}
	if err := p.CheckExecutable("ls"); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckExecutable("/bin/cat"); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckExecutable("rm"); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("unexpected executable accepted: %v", err)
	}
	allowed := p
	allowed.AllowShell = true
	if err := allowed.CheckShell(); err != nil {
		t.Fatal(err)
	}
}

func TestRootBoundaries(t *testing.T) {
	p := Policy{ReadRoots: []string{"/data"}, WriteRoots: []string{"/work"}}
	if err := p.CheckRoots("/data", false); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckRoots("/data/inner/file", false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/database", "/data2", "/etc", "/data/../etc"} {
		if err := p.CheckRoots(path, false); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("%s: accepted", path)
		}
	}
	// Read roots do not grant write access.
	if err := p.CheckRoots("/data/file", true); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("write outside write roots accepted")
	}
	if err := p.CheckCwd("/work"); err != nil {
		// No cwd roots configured means unconstrained.
		t.Fatal(err)
	}
	if err := (Policy{CwdRoots: []string{"/work"}}).CheckCwd("/etc"); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("cwd outside roots accepted")
	}
}

func TestBounds(t *testing.T) {
	p := Policy{MaxTimeoutSeconds: 45, MaxInputBytes: 10, MaxOutputBytes: 20}
	if p.MaxTimeout() != 45*time.Second {
		t.Fatalf("%v", p.MaxTimeout())
	}
	if err := p.CheckInput(11); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("oversized input accepted")
	}
	if err := p.CheckInput(10); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckOutput(21); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("oversized output accepted")
	}
}
