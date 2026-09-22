package tmux

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
	results  []transport.ExecResult
	result   transport.ExecResult
	err      error
}

func (f *fakeTransport) Exec(_ context.Context, _ host.Host, r transport.ExecRequest) (transport.ExecResult, error) {
	f.commands = append(f.commands, r.Command)
	if len(f.results) > 0 {
		result := f.results[0]
		f.results = f.results[1:]
		return result, nil
	}
	return f.result, f.err
}

const testID = "sh_0123456789abcdef0123456789abcdef"
const otherID = "sh_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestListOnlyLiveOwnedSessions(t *testing.T) {
	f := &fakeTransport{result: transport.ExecResult{Stdout: "ash-" + testID + "\npersonal\nash-" + otherID + "\nash-invalid\n"}}
	ids, err := New(f).List(context.Background(), host.Host{})
	if err != nil || len(ids) != 2 || ids[0] != testID || ids[1] != otherID {
		t.Fatalf("%v %v", ids, err)
	}
}

func TestListToleratesMissingServer(t *testing.T) {
	f := &fakeTransport{result: transport.ExecResult{ExitCode: 1, Stderr: "no server running on /tmp/tmux-1000/ash"}}
	ids, err := New(f).List(context.Background(), host.Host{})
	if err != nil || len(ids) != 0 {
		t.Fatalf("%v %v", ids, err)
	}
	f.result = transport.ExecResult{ExitCode: 1, Stderr: "some other failure"}
	if _, err = New(f).List(context.Background(), host.Host{}); err == nil {
		t.Fatal("unexpected failure accepted")
	}
	f.result = transport.ExecResult{ExitCode: 127, Stderr: "tmux is required"}
	if _, err = New(f).List(context.Background(), host.Host{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("missing tmux: %v", err)
	}
}

func TestListRejectsTruncatedOutput(t *testing.T) {
	f := &fakeTransport{result: transport.ExecResult{StdoutTruncated: true}}
	if _, err := New(f).List(context.Background(), host.Host{}); err == nil {
		t.Fatal("accepted truncated list")
	}
}

func TestInvalidIDNeverExecutes(t *testing.T) {
	f := &fakeTransport{}
	b := New(f)
	for _, op := range []func() error{
		func() error { return b.Send(context.Background(), host.Host{}, "../../personal", "x") },
		func() error { return b.Close(context.Background(), host.Host{}, "personal") },
	} {
		if err := op(); err == nil {
			t.Fatal("accepted invalid ID")
		}
	}
	if len(f.commands) != 0 {
		t.Fatal(f.commands)
	}
}

func TestSendQuotesLiteralInputAndBoundsIt(t *testing.T) {
	f := &fakeTransport{results: []transport.ExecResult{{Stdout: "ash-" + testID + "\n"}, {}}}
	input := "-a ' $(touch nope)\n"
	if err := New(f).Send(context.Background(), host.Host{}, testID, input); err != nil {
		t.Fatal(err)
	}
	last := f.commands[len(f.commands)-1]
	if !strings.Contains(last, "send-keys -t 'ash-"+testID+"' -l -- ") || !strings.Contains(last, `'"'"'`) {
		t.Fatal(last)
	}
	f = &fakeTransport{}
	if err := New(f).Send(context.Background(), host.Host{}, testID, strings.Repeat("x", shell.MaxInputSize+1)); err == nil || len(f.commands) != 0 {
		t.Fatalf("%v %v", err, f.commands)
	}
}

func TestCreateUsesNamespaceAndCwd(t *testing.T) {
	f := &fakeTransport{results: []transport.ExecResult{
		{},                               // existence check: empty list
		{},                               // new-session
		{Stdout: "ash-" + testID + "\n"}, // liveness
	}}
	if err := New(f).Create(context.Background(), host.Host{}, testID, "~/project ' quoted"); err != nil {
		t.Fatal(err)
	}
	create := f.commands[1]
	if !strings.Contains(create, "new-session -d -s 'ash-"+testID+"'") || !strings.Contains(create, `-c "$HOME"/'project '"'"' quoted'`) {
		t.Fatal(create)
	}
	// Duplicate creation is refused.
	f = &fakeTransport{results: []transport.ExecResult{{Stdout: "ash-" + testID + "\n"}}}
	if err := New(f).Create(context.Background(), host.Host{}, testID, ""); err == nil {
		t.Fatal("duplicate create accepted")
	}
}

func TestReadAndCloseIdempotency(t *testing.T) {
	f := &fakeTransport{results: []transport.ExecResult{
		{Stdout: "ash-" + testID + "\n"}, // live
		{Stdout: "screen output\n"},      // capture-pane
	}}
	output, err := New(f).Read(context.Background(), host.Host{}, testID, shell.ReadRequest{})
	if err != nil || output.Content != "screen output\n" {
		t.Fatalf("%+v %v", output, err)
	}
	if !strings.Contains(f.commands[1], "capture-pane -p -t 'ash-"+testID+"' -S -") {
		t.Fatal(f.commands[1])
	}
	// Closing an already-gone session succeeds after a real liveness query.
	f = &fakeTransport{results: []transport.ExecResult{
		{},
		{ExitCode: 1, Stderr: "can't find session: ash-" + testID},
	}}
	if err := New(f).Close(context.Background(), host.Host{}, testID); err != nil {
		t.Fatal(err)
	}
}

func TestReadCursorReturnsOnlyNewOutput(t *testing.T) {
	f := &fakeTransport{results: []transport.ExecResult{
		{Stdout: "ash-" + testID + "\n"},
		{Stdout: "first"},
		{Stdout: "ash-" + testID + "\n"},
		{Stdout: "first second"},
	}}
	b := New(f)
	first, err := b.Read(context.Background(), host.Host{}, testID, shell.ReadRequest{})
	if err != nil || first.Content != "first" || first.Cursor == "" {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := b.Read(context.Background(), host.Host{}, testID, shell.ReadRequest{Cursor: first.Cursor})
	if err != nil || second.Content != " second" || second.Resync || second.Cursor == "" {
		t.Fatalf("second: %+v %v", second, err)
	}
}
