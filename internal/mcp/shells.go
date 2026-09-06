package mcp

import (
	"context"

	"ash/internal/service"
	"ash/internal/shell"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type shellCreateInput struct {
	Host string `json:"host"`
	Cwd  string `json:"cwd,omitempty"`
}
type shellHostInput struct {
	Host string `json:"host"`
}
type shellInput struct {
	Host    string `json:"host"`
	ShellID string `json:"shell_id"`
}
type shellSendInput struct {
	Host    string `json:"host"`
	ShellID string `json:"shell_id"`
	Input   string `json:"input" jsonschema:"Literal terminal input; include a newline to execute a command. No newline is added automatically."`
}
type shellListOutput struct {
	Shells []shell.Info `json:"shells"`
}
type shellSendOutput struct {
	Sent bool `json:"sent"`
}
type shellCloseOutput struct {
	Closed bool `json:"closed"`
}

func registerShellTools(server *sdk.Server, s *service.ShellService) {
	sdk.AddTool(server, &sdk.Tool{Name: "ash_shell_create", Description: "Create a persistent shell on a host. Requires exec capability. The shell survives ASH/SSH disconnects."}, func(ctx context.Context, _ *sdk.CallToolRequest, in shellCreateInput) (*sdk.CallToolResult, shell.Info, error) {
		info, err := s.Create(ctx, in.Host, in.Cwd)
		return nil, info, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "ash_shell_list", Description: "List live ASH-owned shells on a host, querying the remote backend. Requires exec capability."}, func(ctx context.Context, _ *sdk.CallToolRequest, in shellHostInput) (*sdk.CallToolResult, shellListOutput, error) {
		shells, err := s.List(ctx, in.Host)
		return nil, shellListOutput{Shells: shells}, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "ash_shell_send", Description: "Send literal UTF-8 input to a persistent shell, at most 64 KiB. Include a newline to execute it. Requires exec capability; returns after delivery, not command completion."}, func(ctx context.Context, _ *sdk.CallToolRequest, in shellSendInput) (*sdk.CallToolResult, shellSendOutput, error) {
		err := s.Send(ctx, in.Host, in.ShellID, in.Input)
		return nil, shellSendOutput{Sent: err == nil}, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "ash_shell_read", Description: "Read a bounded snapshot of shell terminal output and available scrollback. Includes merged stdout/stderr and may repeat previous output. Requires exec capability."}, func(ctx context.Context, _ *sdk.CallToolRequest, in shellInput) (*sdk.CallToolResult, shell.Output, error) {
		output, err := s.Read(ctx, in.Host, in.ShellID)
		return nil, output, err
	})
	sdk.AddTool(server, &sdk.Tool{Name: "ash_shell_close", Description: "Close one ASH-owned persistent shell and clean up its backend session. Requires exec capability."}, func(ctx context.Context, _ *sdk.CallToolRequest, in shellInput) (*sdk.CallToolResult, shellCloseOutput, error) {
		err := s.Close(ctx, in.Host, in.ShellID)
		return nil, shellCloseOutput{Closed: err == nil}, err
	})
}
