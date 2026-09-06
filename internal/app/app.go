// Package app wires configuration, transport and user-facing adapters.
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"ash/internal/cli"
	"ash/internal/config"
	"ash/internal/host"
	ashmcp "ash/internal/mcp"
	"ash/internal/service"
	"ash/internal/shell/zellij"
	sshtransport "ash/internal/transport/ssh"
)

func Run(ctx context.Context, args []string, in io.Reader, out, errout io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(errout, "ash:", err); return 1 }
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintf(out, "ash %s\n", ashmcp.Version)
		return 0
	}
	path, err := config.DefaultPath()
	if err != nil {
		return fail(err)
	}
	if len(args) > 0 && (args[0] == "--config" || strings.HasPrefix(args[0], "--config=")) {
		if args[0] == "--config" {
			if len(args) < 2 {
				return fail(fmt.Errorf("--config requires a path"))
			}
			path = args[1]
			args = args[2:]
		} else {
			path = strings.TrimPrefix(args[0], "--config=")
			args = args[1:]
		}
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return cli.Run(ctx, args, nil, nil, in, out, errout)
	}
	c, err := config.Load(path)
	if err != nil {
		return fail(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fail(err)
	}
	t, err := sshtransport.New(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		return fail(err)
	}
	hosts := host.New(c.Hosts)
	s := service.New(hosts, t)
	shells := service.NewShells(hosts, zellij.New(t))
	if args[0] == "mcp" {
		if len(args) != 1 {
			return fail(fmt.Errorf("mcp takes no arguments"))
		}
		if err := ashmcp.New(s, shells).Run(ctx, &sdk.IOTransport{Reader: readCloser(in), Writer: writeCloser(out)}); err != nil && ctx.Err() == nil {
			return fail(err)
		}
		return 0
	}
	return cli.Run(ctx, args, s, shells, in, out, errout)
}

func readCloser(r io.Reader) io.ReadCloser {
	if c, ok := r.(io.ReadCloser); ok {
		return c
	}
	return io.NopCloser(r)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
func writeCloser(w io.Writer) io.WriteCloser {
	if c, ok := w.(io.WriteCloser); ok {
		return c
	}
	return nopWriteCloser{w}
}
