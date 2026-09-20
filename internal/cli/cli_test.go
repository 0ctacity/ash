package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
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

type fileTransport struct {
	transport.Transport
	atomicData []byte
	list       []transport.DirEntry
	readTree   []transport.TreeEntry
	writeRoot  string
	writeTree  []transport.TreeEntry
}

func (f *fileTransport) List(context.Context, host.Host, string) ([]transport.DirEntry, error) {
	return f.list, nil
}
func (f *fileTransport) Mkdir(context.Context, host.Host, string) error          { return nil }
func (f *fileTransport) Rename(context.Context, host.Host, string, string) error { return nil }
func (f *fileTransport) Remove(context.Context, host.Host, string) error         { return nil }
func (f *fileTransport) AtomicWrite(_ context.Context, _ host.Host, _ string, data []byte) error {
	f.atomicData = data
	return nil
}
func (f *fileTransport) ReadTree(context.Context, host.Host, string) ([]transport.TreeEntry, error) {
	return f.readTree, nil
}
func (f *fileTransport) WriteTree(_ context.Context, _ host.Host, root string, entries []transport.TreeEntry) error {
	f.writeRoot = root
	f.writeTree = entries
	return nil
}

func TestFileCLIOperations(t *testing.T) {
	f := &fileTransport{list: []transport.DirEntry{{Name: "a", IsDir: false}}}
	s := service.New(host.New(map[string]host.Host{"h": {Policy: policy.Policy{Read: true, Write: true}}}), f)
	var out, errout bytes.Buffer
	run := func(args ...string) int {
		out.Reset()
		errout.Reset()
		return Run(context.Background(), args, s, nil, strings.NewReader(""), &out, &errout)
	}
	if code := run("list", "h", "/d"); code != 0 || !strings.Contains(out.String(), `"name":"a"`) {
		t.Fatalf("list: %d %q %q", code, out.String(), errout.String())
	}
	if code := Run(context.Background(), []string{"write", "h", "/p", "--atomic"}, s, nil, strings.NewReader("data"), &out, &errout); code != 0 || string(f.atomicData) != "data" {
		t.Fatalf("atomic write: %d %q", code, f.atomicData)
	}
	if code := run("mkdir", "h", "/d"); code != 0 || !strings.Contains(out.String(), `"ok":true`) {
		t.Fatalf("mkdir: %d %q", code, out.String())
	}
	if code := run("rename", "h", "/a", "/b"); code != 0 {
		t.Fatalf("rename: %d %q", code, errout.String())
	}
	if code := run("remove", "h", "/a"); code != 0 {
		t.Fatalf("remove: %d %q", code, errout.String())
	}

	local := t.TempDir()
	f.readTree = []transport.TreeEntry{{Path: "sub", IsDir: true}, {Path: "sub/x.txt", Data: []byte("hi")}, {Path: "top.txt", Data: []byte("top")}}
	if code := run("download", "h", "/remote", local); code != 0 {
		t.Fatalf("download: %d %q", code, errout.String())
	}
	if data, err := os.ReadFile(filepath.Join(local, "sub", "x.txt")); err != nil || string(data) != "hi" {
		t.Fatalf("downloaded %q %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(local, "top.txt")); err != nil || string(data) != "top" {
		t.Fatalf("downloaded top %q %v", data, err)
	}

	upload := t.TempDir()
	if err := os.MkdirAll(filepath.Join(upload, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upload, "nested", "y.txt"), []byte("yo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run("upload", "h", upload, "/remote"); code != 0 {
		t.Fatalf("upload: %d %q", code, errout.String())
	}
	if f.writeRoot != "/remote" {
		t.Fatalf("root %q", f.writeRoot)
	}
	found := false
	for _, entry := range f.writeTree {
		if entry.Path == "nested/y.txt" && string(entry.Data) == "yo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("upload entries %+v", f.writeTree)
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
