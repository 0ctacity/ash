package ssh

import (
	"ash/internal/transport"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCommandEscapesInputsAndPreservesShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local POSIX shell execution is covered by Unix CI runners")
	}
	value := "a ' $(echo injected);\nvalue"
	command, err := buildCommand(transport.ExecRequest{Command: `printf '%s' "$VALUE"; exit 7`, Env: map[string]string{"VALUE": value}, Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-c", command).Output()
	if string(out) != value {
		t.Fatalf("output %q", out)
	}
	if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 7 {
		t.Fatalf("exit %v", err)
	}
	for _, key := range []string{"", "1BAD", "A;touch /tmp/bad", "A=B"} {
		if _, err := buildCommand(transport.ExecRequest{Command: "true", Env: map[string]string{key: "x"}}); err == nil {
			t.Errorf("accepted %q", key)
		}
	}
}
func TestBoundedOutput(t *testing.T) {
	b := limitedBuffer{limit: 4}
	n, err := b.Write([]byte("abcdef"))
	if err != nil || n != 6 || b.String() != "abcd" || !b.truncated {
		t.Fatalf("%+v %d %v", b, n, err)
	}
	b.Write([]byte(strings.Repeat("x", 100)))
	if b.Len() != 4 {
		t.Fatal(b.Len())
	}
}

func TestNewDefersTrustFileUntilOperation(t *testing.T) {
	if _, err := New("/nonexistent/known_hosts"); err != nil {
		t.Fatal(err)
	}
}

func TestCommandQuotesCwdAndExpandsRemoteHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local POSIX shell execution is covered by Unix CI runners")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "space ' $(printf injected)")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{dir, "~/" + filepath.Base(dir)} {
		command, err := buildCommand(transport.ExecRequest{Cwd: cwd, Command: "pwd -P"})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", command)
		cmd.Env = append(os.Environ(), "HOME="+root)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		physical, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(out)) != physical {
			t.Fatalf("cwd %q: got %q want %q", cwd, out, physical)
		}
	}
}
