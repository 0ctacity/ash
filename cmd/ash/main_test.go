package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStdioMCP(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "ash")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	config := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(config, []byte("[hosts.fixture]\naddress='localhost'\nuser='test'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.Command(binary, "--config", config, "mcp")
	cmd.Env = append(os.Environ(), "HOME="+dir)
	cmd.Stderr = os.Stderr
	client := sdk.NewClient(&sdk.Implementation{Name: "ash-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 10 {
		t.Fatalf("tools: %+v %v", tools, err)
	}
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "ash_hosts", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("hosts: %+v %v", result, err)
	}
	result, err = session.CallTool(ctx, &sdk.CallToolParams{Name: "ash_exec", Arguments: map[string]any{"host": "fixture", "command": "true"}})
	if err != nil || !result.IsError {
		t.Fatalf("denied exec: %+v %v", result, err)
	}
}
