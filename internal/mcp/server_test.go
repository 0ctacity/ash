package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"ash/internal/host"
	"ash/internal/policy"
	"ash/internal/service"
	"ash/internal/shell"
	"ash/internal/transport"
)

func TestToolsAndDeniedExec(t *testing.T) {
	ctx := context.Background()
	server := New(service.New(host.New(map[string]host.Host{"h": {}}), nil), nil)
	st, ct := sdk.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 10 {
		t.Fatalf("%+v %v", list, err)
	}
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "ash_hosts", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "ash_exec", Arguments: map[string]any{"host": "h", "command": "true"}})
	if err != nil || !res.IsError {
		t.Fatalf("%+v %v", res, err)
	}
}

type cancelTransport struct {
	transport.Transport
	started  chan struct{}
	canceled chan struct{}
}

func (f *cancelTransport) Exec(ctx context.Context, _ host.Host, _ transport.ExecRequest) (transport.ExecResult, error) {
	close(f.started)
	<-ctx.Done()
	close(f.canceled)
	return transport.ExecResult{}, ctx.Err()
}
func TestClientCancellationReachesService(t *testing.T) {
	ctx, cancelAll := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelAll()
	tr := &cancelTransport{started: make(chan struct{}), canceled: make(chan struct{})}
	server := New(service.New(host.New(map[string]host.Host{"h": {Policy: policy.Policy{Exec: true}}}), tr), nil)
	st, ct := sdk.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		cs.CallTool(callCtx, &sdk.CallToolParams{Name: "ash_exec", Arguments: map[string]any{"host": "h", "command": "sleep 10"}})
	}()
	select {
	case <-tr.started:
	case <-ctx.Done():
		t.Fatal("tool did not start")
	}
	cancel()
	select {
	case <-tr.canceled:
	case <-ctx.Done():
		t.Fatal("cancellation did not reach transport")
	}
	<-done
}

type persistentBackend struct {
	shell.Backend
	id, input string
}

func (b *persistentBackend) Name() string { return "test" }
func (b *persistentBackend) Create(_ context.Context, _ host.Host, id, cwd string) error {
	b.id = id
	return nil
}
func (b *persistentBackend) List(context.Context, host.Host) ([]string, error) {
	if b.id == "" {
		return []string{}, nil
	}
	return []string{b.id}, nil
}
func (b *persistentBackend) Send(_ context.Context, _ host.Host, id, input string) error {
	b.input = input
	return nil
}
func (b *persistentBackend) Read(context.Context, host.Host, string) (shell.Output, error) {
	return shell.Output{Content: b.input}, nil
}
func (b *persistentBackend) Close(context.Context, host.Host, string) error { b.id = ""; return nil }

func TestPersistentShellTools(t *testing.T) {
	ctx := context.Background()
	backend := new(persistentBackend)
	hosts := host.New(map[string]host.Host{"h": {Policy: policy.Policy{Exec: true}}, "denied": {}})
	server := New(service.New(hosts, nil), service.NewShells(hosts, backend))
	st, ct := sdk.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "shell-test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		result, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatal(result.GetError())
		}
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]any
		if err = json.Unmarshal(data, &obj); err != nil {
			t.Fatal(err)
		}
		return obj
	}
	created := call("ash_shell_create", map[string]any{"host": "h", "cwd": "/tmp"})
	id := created["id"]
	if id != backend.id || created["backend"] != "test" {
		t.Fatal(created)
	}
	list := call("ash_shell_list", map[string]any{"host": "h"})
	if len(list["shells"].([]any)) != 1 {
		t.Fatal(list)
	}
	call("ash_shell_send", map[string]any{"host": "h", "shell_id": id, "input": "pwd\n"})
	output := call("ash_shell_read", map[string]any{"host": "h", "shell_id": id})
	if output["content"] != "pwd\n" || output["truncated"] != false {
		t.Fatal(output)
	}
	call("ash_shell_close", map[string]any{"host": "h", "shell_id": id})
	list = call("ash_shell_list", map[string]any{"host": "h"})
	if len(list["shells"].([]any)) != 0 {
		t.Fatal(list)
	}
	denied, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "ash_shell_create", Arguments: map[string]any{"host": "denied"}})
	if err != nil || !denied.IsError {
		t.Fatalf("%v %v", denied, err)
	}
}
