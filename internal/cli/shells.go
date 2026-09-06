package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"ash/internal/service"
	"ash/internal/shell"
)

func runShell(ctx context.Context, args []string, s *service.ShellService, in io.Reader, out, errout io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("shell requires create|list|send|read|close HOST")
	}
	operation, name := args[0], args[1]
	switch operation {
	case "create":
		cwd := ""
		if len(args) == 4 && args[2] == "--cwd" {
			cwd = args[3]
		} else if len(args) == 3 && strings.HasPrefix(args[2], "--cwd=") {
			cwd = strings.TrimPrefix(args[2], "--cwd=")
		} else if len(args) != 2 {
			return fmt.Errorf("shell create requires HOST [--cwd PATH]")
		}
		info, err := s.Create(ctx, name, cwd)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(info)
	case "list":
		if len(args) != 2 {
			return fmt.Errorf("shell list requires HOST")
		}
		shells, err := s.List(ctx, name)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(shells)
	case "send":
		if len(args) < 3 {
			return fmt.Errorf("shell send requires HOST ID [INPUT]")
		}
		var input string
		switch {
		case len(args) == 3:
			data, err := readInput(ctx, in, shell.MaxInputSize)
			if err != nil {
				return err
			}
			input = string(data)
		case len(args) == 4:
			input = args[3]
		case len(args) == 5 && args[3] == "--":
			input = args[4]
		default:
			return fmt.Errorf("shell send requires HOST ID [INPUT]; omit INPUT to read stdin")
		}
		return s.Send(ctx, name, args[2], input)
	case "read":
		if len(args) != 3 {
			return fmt.Errorf("shell read requires HOST ID")
		}
		output, err := s.Read(ctx, name, args[2])
		if err != nil {
			return err
		}
		if _, err = io.WriteString(out, output.Content); err != nil {
			return err
		}
		if output.Truncated {
			_, err = fmt.Fprintln(errout, "ash: shell output truncated")
		}
		return err
	case "close":
		if len(args) != 3 {
			return fmt.Errorf("shell close requires HOST ID")
		}
		return s.Close(ctx, name, args[2])
	default:
		return fmt.Errorf("unknown shell operation %q", operation)
	}
}

// readInput bounds input and interrupts a pipe read when the process is canceled.
func readInput(ctx context.Context, in io.Reader, limit int) ([]byte, error) {
	stop := func() bool { return true }
	if closer, ok := in.(io.Closer); ok {
		stop = context.AfterFunc(ctx, func() { closer.Close() })
	}
	data, err := io.ReadAll(io.LimitReader(in, int64(limit)+1))
	stop()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("input exceeds %d byte limit", limit)
	}
	return data, nil
}
