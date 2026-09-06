package zellij

import (
	"ash/internal/host"
	"ash/internal/shell"
	"ash/internal/transport"
	"context"
	"strings"
	"testing"
)

type fakeTransport struct {
	transport.Transport
	commands []string
	result   transport.ExecResult
	panes    string
	err      error
	results  []transport.ExecResult
}

func (f *fakeTransport) Exec(_ context.Context, _ host.Host, r transport.ExecRequest) (transport.ExecResult, error) {
	f.commands = append(f.commands, r.Command)
	if len(f.results) > 0 {
		result := f.results[0]
		f.results = f.results[1:]
		return result, nil
	}
	if strings.Contains(r.Command, "list-panes") && f.panes != "" {
		return transport.ExecResult{Stdout: f.panes}, nil
	}
	return f.result, f.err
}

const testID = "sh_0123456789abcdef0123456789abcdef"

func TestListOnlyLiveOwnedSessions(t *testing.T) {
	f := &fakeTransport{result: transport.ExecResult{Stdout: "other [Created now]\nash-" + testID + " [Created now]\nash-sh_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa [Created ago] (EXITED - attach to resurrect)\nash-invalid [Created now]\n"}}
	ids, err := New(f).List(context.Background(), host.Host{})
	if err != nil || len(ids) != 1 || ids[0] != testID {
		t.Fatalf("%v %v", ids, err)
	}
}
func TestInvalidIDNeverExecutes(t *testing.T) {
	f := &fakeTransport{}
	b := New(f)
	if err := b.Send(context.Background(), host.Host{}, "../../personal", "x"); err == nil {
		t.Fatal("accepted invalid ID")
	}
	if len(f.commands) != 0 {
		t.Fatal(f.commands)
	}
}
func TestSendQuotesLiteralInput(t *testing.T) {
	f := &fakeTransport{result: transport.ExecResult{Stdout: "ash-" + testID + " [Created now]\n"}}
	f.panes = `[{"id":7,"is_plugin":false}]`
	input := "-a ' $(touch nope)\n"
	if err := New(f).Send(context.Background(), host.Host{}, testID, input); err != nil {
		t.Fatal(err)
	}
	last := f.commands[len(f.commands)-1]
	if !strings.Contains(last, "write-chars --pane-id terminal_7 -- ") || !strings.Contains(last, `'"'"'`) {
		t.Fatal(last)
	}
}
func TestSendBoundsInput(t *testing.T) {
	f := &fakeTransport{}
	if err := New(f).Send(context.Background(), host.Host{}, testID, strings.Repeat("x", shell.MaxInputSize+1)); err == nil || len(f.commands) != 0 {
		t.Fatalf("%v %v", err, f.commands)
	}
}

func TestEmptyAndTruncatedLists(t *testing.T) {
	f := &fakeTransport{result: transport.ExecResult{ExitCode: 1, Stderr: "No active zellij sessions found."}}
	ids, err := New(f).List(context.Background(), host.Host{})
	if err != nil || len(ids) != 0 {
		t.Fatalf("%v %v", ids, err)
	}
	f.result = transport.ExecResult{StdoutTruncated: true}
	if _, err = New(f).List(context.Background(), host.Host{}); err == nil {
		t.Fatal("accepted truncated list")
	}
	f.result = transport.ExecResult{ExitCode: 1, Stderr: "No active zellij sessions found."}
	f.err = context.Canceled
	if _, err = New(f).List(context.Background(), host.Host{}); err != context.Canceled {
		t.Fatalf("suppressed cancellation: %v", err)
	}
}

func TestCloseAbsentShellCleansOnlyItsConfig(t *testing.T) {
	f := &fakeTransport{results: []transport.ExecResult{{}, {ExitCode: 2, Stderr: `Session: "ash-` + testID + `" not found.`}, {}}}
	if err := New(f).Close(context.Background(), host.Host{}, testID); err != nil {
		t.Fatal(err)
	}
	if len(f.commands) != 3 || !strings.Contains(f.commands[2], "/shells/"+testID+"/config.kdl") {
		t.Fatal(f.commands)
	}
}
