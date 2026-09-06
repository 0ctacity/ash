// Package cli provides the command-line adapter for ASH services.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"ash/internal/service"
	"ash/internal/transport"
)

const Usage = `Usage:
  ash --version
  ash [--config PATH] hosts
  ash [--config PATH] exec HOST [--cwd PATH] [--env KEY=VALUE] [--timeout 30s] -- COMMAND...
  ash [--config PATH] read HOST PATH
  ash [--config PATH] write HOST PATH < FILE
  ash [--config PATH] stat HOST PATH
  ash [--config PATH] shell create HOST [--cwd PATH]
  ash [--config PATH] shell list HOST
  ash [--config PATH] shell send HOST ID [INPUT]
  ash [--config PATH] shell read HOST ID
  ash [--config PATH] shell close HOST ID
  ash [--config PATH] mcp

COMMAND is shell code executed through the remote user's shell.
Quote remote ~/ paths to prevent your local shell from expanding them.
Shell send reads stdin if INPUT is omitted. Include a newline to execute input.
Shell read returns a terminal snapshot, not an incremental log.
`

func Run(ctx context.Context, args []string, s *service.Service, shells *service.ShellService, in io.Reader, out, errout io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(errout, "ash:", err); return 1 }
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, Usage)
		return 0
	}
	switch args[0] {
	case "shell":
		if err := runShell(ctx, args[1:], shells, in, out, errout); err != nil {
			return fail(err)
		}
		return 0
	case "hosts":
		if len(args) != 1 {
			return fail(fmt.Errorf("hosts takes no arguments"))
		}
		if err := json.NewEncoder(out).Encode(s.Hosts()); err != nil {
			return fail(err)
		}
		return 0
	case "exec":
		req, err := parseExec(args[1:])
		if err != nil {
			return fail(err)
		}
		r, err := s.Exec(ctx, req)
		if err != nil {
			return fail(err)
		}
		if _, err := io.WriteString(out, r.Stdout); err != nil {
			return fail(err)
		}
		if _, err := io.WriteString(errout, r.Stderr); err != nil {
			return fail(err)
		}
		if r.StdoutTruncated {
			fmt.Fprintln(errout, "\nash: stdout truncated at 8 MiB")
		}
		if r.StderrTruncated {
			fmt.Fprintln(errout, "\nash: stderr truncated at 8 MiB")
		}
		if r.ExitCode < 0 || r.ExitCode > 255 {
			return 1
		}
		return r.ExitCode
	case "read", "write", "stat":
		if len(args) != 3 {
			return fail(fmt.Errorf("%s requires HOST PATH", args[0]))
		}
		name, path := args[1], args[2]
		switch args[0] {
		case "read":
			data, err := s.Read(ctx, name, path)
			if err != nil {
				return fail(err)
			}
			if _, err := out.Write(data); err != nil {
				return fail(err)
			}
		case "write":
			data, err := readInput(ctx, in, transport.MaxWriteSize)
			if err != nil {
				return fail(err)
			}
			if err := s.Write(ctx, name, path, data); err != nil {
				return fail(err)
			}
		case "stat":
			info, err := s.Stat(ctx, name, path)
			if err != nil {
				return fail(err)
			}
			if err := json.NewEncoder(out).Encode(info); err != nil {
				return fail(err)
			}
		}
		return 0
	default:
		return fail(fmt.Errorf("unknown command %q", args[0]))
	}
}

func parseExec(args []string) (transport.ExecRequest, error) {
	var req transport.ExecRequest
	if len(args) == 0 {
		return req, fmt.Errorf("exec requires HOST [flags] -- COMMAND")
	}
	req.Host = args[0]
	req.Env = make(map[string]string)
	for i := 1; i < len(args); i++ {
		if args[i] == "--" {
			if i+1 == len(args) {
				return req, fmt.Errorf("command must not be empty")
			}
			req.Command = strings.Join(args[i+1:], " ")
			return req, nil
		}
		key, value, hasValue := strings.Cut(args[i], "=")
		if key != "--cwd" && key != "--env" && key != "--timeout" {
			return req, fmt.Errorf("unknown exec option %q; put command after --", key)
		}
		if !hasValue {
			i++
			if i == len(args) {
				return req, fmt.Errorf("%s requires a value", key)
			}
			value = args[i]
		}
		switch key {
		case "--cwd":
			req.Cwd = value
		case "--env":
			name, v, ok := strings.Cut(value, "=")
			if !ok || name == "" {
				return req, fmt.Errorf("--env requires KEY=VALUE")
			}
			req.Env[name] = v
		case "--timeout":
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return req, fmt.Errorf("--timeout requires a positive duration")
			}
			req.Timeout = d
		}
	}
	return req, fmt.Errorf("exec requires -- before command")
}
