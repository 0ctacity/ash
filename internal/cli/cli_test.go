package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"ash/internal/host"
	"ash/internal/policy"
	"ash/internal/service"
	"ash/internal/shell"
	"ash/internal/transport"
)

type recorder struct {
	transport.Transport
	request transport.ExecRequest
}

func (r *recorder) Exec(_ context.Context, _ host.Host, req transport.ExecRequest) (transport.ExecResult, error) {
	r.request = req
	return transport.ExecResult{ExitCode: 9, Stdout: "out", Stderr: "err"}, nil
}
func TestExecPreservesCommandAndExit(t *testing.T) {
	r := new(recorder)
	s := service.New(host.New(map[string]host.Host{"h": {Policy: policy.Policy{Exec: true}}}), r)
	var out, errout bytes.Buffer
	code := Run(context.Background(), []string{"exec", "h", "--cwd", "~/a b", "--env", "X=a=b", "--timeout", "2s", "--", "printf '%s' \"$X\"; exit 9"}, s, nil, strings.NewReader(""), &out, &errout)
	if code != 9 || out.String() != "out" || errout.String() != "err" {
		t.Fatalf("%d %q %q", code, out.String(), errout.String())
	}
	if r.request.Command != "printf '%s' \"$X\"; exit 9" || r.request.Cwd != "~/a b" || r.request.Env["X"] != "a=b" {
		t.Fatalf("%+v", r.request)
	}
}
func TestExecRequiresDelimiter(t *testing.T) {
	var out bytes.Buffer
	if c := Run(context.Background(), []string{"exec", "h", "true"}, nil, nil, strings.NewReader(""), &out, &out); c != 1 {
		t.Fatal(c)
	}
}

func TestWriteCancellationInterruptsStdin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	var out, errout bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- Run(ctx, []string{"write", "h", "/x"}, nil, nil, reader, &out, &errout) }()
	cancel()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatal(code)
		}
	case <-time.After(time.Second):
		t.Fatal("write remained blocked on stdin after cancellation")
	}
}

type shellRecorder struct {
	shell.Backend
	input, cwd, id string
}

func (r *shellRecorder) Name() string { return "test" }
func (r *shellRecorder) Create(_ context.Context, _ host.Host, id, cwd string) error {
	r.cwd = cwd
	r.id = id
	return nil
}
func (r *shellRecorder) Send(_ context.Context, _ host.Host, id, input string) error {
	r.input = input
	return nil
}
func TestShellCLI(t *testing.T) {
	r := new(shellRecorder)
	shells := service.NewShells(host.New(map[string]host.Host{"h": {Policy: policy.Policy{Exec: true}}}), r)
	var out, errout bytes.Buffer
	code := Run(context.Background(), []string{"shell", "create", "h", "--cwd", "~/a b"}, nil, shells, strings.NewReader(""), &out, &errout)
	if code != 0 || r.cwd != "~/a b" || !strings.Contains(out.String(), `"id":"sh_`) {
		t.Fatalf("%d %q %q", code, out.String(), errout.String())
	}
	id := "sh_" + strings.Repeat("a", 32)
	code = Run(context.Background(), []string{"shell", "send", "h", id}, nil, shells, strings.NewReader("pwd\n"), &out, &errout)
	if code != 0 || r.input != "pwd\n" {
		t.Fatalf("%d %q %q", code, r.input, errout.String())
	}
	code = Run(context.Background(), []string{"shell", "send", "h", id, "--", "-literal"}, nil, shells, strings.NewReader("unused"), &out, &errout)
	if code != 0 || r.input != "-literal" {
		t.Fatalf("%d %q", code, r.input)
	}
}

func (r *shellRecorder) List(context.Context, host.Host) ([]string, error) {
	if r.id == "" {
		return []string{}, nil
	}
	return []string{r.id}, nil
}
func (r *shellRecorder) Read(context.Context, host.Host, string) (shell.Output, error) {
	return shell.Output{Content: r.input, Truncated: true}, nil
}
func (r *shellRecorder) Close(context.Context, host.Host, string) error { r.id = ""; return nil }
func TestShellCLIListReadCloseAndInputLimit(t *testing.T) {
	id := "sh_" + strings.Repeat("b", 32)
	r := &shellRecorder{id: id, input: "captured output"}
	shells := service.NewShells(host.New(map[string]host.Host{"h": {Policy: policy.Policy{Exec: true}}}), r)
	var out, errout bytes.Buffer
	run := func(args ...string) int {
		out.Reset()
		errout.Reset()
		return Run(context.Background(), args, nil, shells, strings.NewReader(""), &out, &errout)
	}
	if code := run("shell", "list", "h"); code != 0 || !strings.Contains(out.String(), id) {
		t.Fatalf("%d %q %q", code, out.String(), errout.String())
	}
	if code := run("shell", "read", "h", id); code != 0 || out.String() != "captured output" || !strings.Contains(errout.String(), "truncated") {
		t.Fatalf("%d %q %q", code, out.String(), errout.String())
	}
	if code := Run(context.Background(), []string{"shell", "send", "h", id}, nil, shells, strings.NewReader(strings.Repeat("a", shell.MaxInputSize+1)), &out, &errout); code != 1 || r.input != "captured output" {
		t.Fatalf("%d input mutated", code)
	}
	if code := run("shell", "close", "h", id); code != 0 || r.id != "" {
		t.Fatalf("%d %q", code, r.id)
	}
	if code := run("shell", "list", "h"); code != 0 || out.String() != "[]\n" {
		t.Fatalf("%d %q", code, out.String())
	}
}
