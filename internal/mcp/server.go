// Package mcp adapts the shared services to the MCP stdio protocol.
package mcp

import (
	"context"
	"encoding/base64"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"ash/internal/service"
	"ash/internal/transport"
)

// Version is overridden by the release build and reported by both CLI and MCP.
var Version = "dev"

type hostsResult struct {
	Hosts []service.HostInfo `json:"hosts"`
}
type execInput struct {
	Host      string            `json:"host"`
	Command   string            `json:"command" jsonschema:"Shell code executed through the remote user's shell"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMS int64             `json:"timeout_ms,omitempty"`
	Stdin     *string           `json:"stdin,omitempty" jsonschema:"UTF-8 text forwarded to the remote process stdin, at most 64 KiB. Set it to an empty string to send empty input."`
	StdinB64  *string           `json:"stdin_base64,omitempty" jsonschema:"Base64-encoded binary forwarded to the remote process stdin, at most 64 KiB. Mutually exclusive with stdin."`
}
type execOutput struct {
	ExitCode        int    `json:"exit_code"`
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	DurationMS      int64  `json:"duration_ms"`
}
type fileInput struct {
	Host string `json:"host"`
	Path string `json:"path"`
}
type writeInput struct {
	Host    string `json:"host"`
	Path    string `json:"path"`
	Content string `json:"content"`
}
type readOutput struct {
	Content string `json:"content"`
	Size    int    `json:"size"`
}
type writeOutput struct {
	Size int `json:"size"`
}

// execStdin resolves the mutually exclusive text and binary stdin fields.
func execStdin(in execInput) ([]byte, bool, error) {
	if in.Stdin != nil && in.StdinB64 != nil {
		return nil, false, fmt.Errorf("provide only one of stdin or stdin_base64")
	}
	if in.Stdin != nil {
		return []byte(*in.Stdin), true, nil
	}
	if in.StdinB64 != nil {
		data, err := base64.StdEncoding.DecodeString(*in.StdinB64)
		if err != nil {
			return nil, false, fmt.Errorf("stdin_base64 is not valid base64: %w", err)
		}
		return data, true, nil
	}
	return nil, false, nil
}

func New(s *service.Service, shells *service.ShellService) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "ash", Version: Version}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "ash_hosts", Description: "List configured hosts and granted capabilities."}, func(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, hostsResult, error) {
		return nil, hostsResult{Hosts: s.Hosts()}, nil
	})
	sdk.AddTool(server, &sdk.Tool{Name: "ash_exec", Description: "Execute shell code on a configured host. Non-zero process exits are results, not tool errors."}, func(ctx context.Context, _ *sdk.CallToolRequest, in execInput) (*sdk.CallToolResult, execOutput, error) {
		timeout, err := service.TimeoutMillis(in.TimeoutMS)
		if err != nil {
			return nil, execOutput{}, err
		}
		stdin, stdinSet, err := execStdin(in)
		if err != nil {
			return nil, execOutput{}, err
		}
		r, err := s.Exec(ctx, transport.ExecRequest{Host: in.Host, Command: in.Command, Cwd: in.Cwd, Env: in.Env, Timeout: timeout, Stdin: stdin, StdinSet: stdinSet})
		return nil, execOutput{ExitCode: r.ExitCode, Stdout: r.Stdout, Stderr: r.Stderr, StdoutTruncated: r.StdoutTruncated, StderrTruncated: r.StderrTruncated, DurationMS: r.Duration.Milliseconds()}, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "ash_read", Description: "Read up to 4 MiB of UTF-8 text using SFTP."}, func(ctx context.Context, _ *sdk.CallToolRequest, in fileInput) (*sdk.CallToolResult, readOutput, error) {
		data, err := s.Read(ctx, in.Host, in.Path)
		if err != nil {
			return nil, readOutput{}, err
		}
		content, err := service.Text(data)
		return nil, readOutput{Content: content, Size: len(data)}, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "ash_write", Description: "Create or truncate a file through SFTP, up to 4 MiB. Parent directory must exist."}, func(ctx context.Context, _ *sdk.CallToolRequest, in writeInput) (*sdk.CallToolResult, writeOutput, error) {
		err := s.Write(ctx, in.Host, in.Path, []byte(in.Content))
		return nil, writeOutput{Size: len(in.Content)}, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "ash_stat", Description: "Inspect file metadata through SFTP. Requires read capability."}, func(ctx context.Context, _ *sdk.CallToolRequest, in fileInput) (*sdk.CallToolResult, transport.FileInfo, error) {
		r, err := s.Stat(ctx, in.Host, in.Path)
		return nil, r, err
	})
	registerShellTools(server, shells)
	return server
}
