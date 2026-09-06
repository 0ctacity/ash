package ssh

import (
	"ash/internal/host"
	"ash/internal/transport"
	"bytes"
	"context"
	"errors"
	"fmt"
	gossh "golang.org/x/crypto/ssh"
	"regexp"
	"sort"
	"strings"
	"time"
)

// QuoteShell quotes one literal POSIX shell word.
func QuoteShell(value string) string { return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'" }

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func buildCommand(req transport.ExecRequest) (string, error) {
	if strings.ContainsRune(req.Command, 0) || strings.ContainsRune(req.Cwd, 0) {
		return "", fmt.Errorf("command and cwd must not contain NUL")
	}
	var b strings.Builder
	keys := make([]string, 0, len(req.Env))
	for k := range req.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := req.Env[k]
		if !envName.MatchString(k) || strings.ContainsRune(v, 0) {
			return "", fmt.Errorf("invalid environment variable %q", k)
		}
		fmt.Fprintf(&b, "export %s=%s\n", k, QuoteShell(v))
	}
	if req.Cwd != "" {
		cwd := QuoteShell(req.Cwd)
		if req.Cwd == "~" {
			cwd = `"$HOME"`
		} else if strings.HasPrefix(req.Cwd, "~/") {
			cwd = `"$HOME"/` + QuoteShell(req.Cwd[2:])
		}
		fmt.Fprintf(&b, "cd -- %s || exit\n", cwd)
	}
	b.WriteString(req.Command)
	return b.String(), nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Len() int       { return b.buffer.Len() }
func (b *limitedBuffer) String() string { return b.buffer.String() }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if n > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	b.buffer.Write(p)
	return n, nil
}
func (t *Transport) Exec(ctx context.Context, h host.Host, req transport.ExecRequest) (result transport.ExecResult, err error) {
	started := time.Now()
	defer func() { result.Duration = time.Since(started) }()
	command, err := buildCommand(req)
	if err != nil {
		return result, err
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = transport.DefaultExecTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, cleanup, err := t.connect(ctx, h)
	if err != nil {
		return result, err
	}
	defer cleanup()
	session, err := client.NewSession()
	if err != nil {
		return result, operationError(ctx, err)
	}
	defer session.Close()
	stdout := limitedBuffer{limit: transport.MaxOutputSize}
	stderr := limitedBuffer{limit: transport.MaxOutputSize}
	session.Stdout = &stdout
	session.Stderr = &stderr
	err = session.Run(command)
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.StdoutTruncated = stdout.truncated
	result.StderrTruncated = stderr.truncated
	if ctx.Err() != nil {
		return result, operationError(ctx, err)
	}
	var exit *gossh.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitStatus()
		return result, nil
	}
	return result, err
}
