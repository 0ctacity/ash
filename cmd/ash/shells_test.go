package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestPersistentShellMCPReconnect verifies two independent ASH processes control
// the same remote shell. It is opt-in because it creates one remote Zellij session.
func TestPersistentShellMCPReconnect(t *testing.T) {
	config := os.Getenv("ASH_ZELLIJ_CONFIG")
	if config == "" {
		t.Skip("set ASH_ZELLIJ_CONFIG to test persistent shells through MCP on a configured host")
	}
	name := os.Getenv("ASH_ZELLIJ_HOST")
	if name == "" {
		name = "fedora"
	}
	binary := filepath.Join(t.TempDir(), "ash")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	connect := func() *sdk.ClientSession {
		t.Helper()
		cmd := exec.Command(binary, "--config", config, "mcp")
		cmd.Stderr = os.Stderr
		session, err := sdk.NewClient(&sdk.Implementation{Name: "shell-reconnect-test", Version: "1"}, nil).Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	call := func(session *sdk.ClientSession, name string, args map[string]any) map[string]any {
		t.Helper()
		result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatal(shellToolError(result))
		}
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err = json.Unmarshal(data, &object); err != nil {
			t.Fatal(err)
		}
		return object
	}
	first := connect()
	defer first.Close()
	created := call(first, "ash_shell_create", map[string]any{"host": name, "cwd": "/tmp"})
	id, ok := created["id"].(string)
	if !ok || id == "" {
		t.Fatal(created)
	}
	closed := false
	// Cleanup reconnects independently, including if the test failed after disconnect.
	t.Cleanup(func() {
		if closed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cleanupCancel()
		cmd := exec.Command(binary, "--config", config, "mcp")
		cmd.Stderr = os.Stderr
		client := sdk.NewClient(&sdk.Implementation{Name: "shell-cleanup", Version: "1"}, nil)
		session, err := client.Connect(cleanupCtx, &sdk.CommandTransport{Command: cmd}, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer session.Close()
		result, err := session.CallTool(cleanupCtx, &sdk.CallToolParams{Name: "ash_shell_close", Arguments: map[string]any{"host": name, "shell_id": id}})
		if err != nil {
			t.Error(err)
		} else if result.IsError && !strings.Contains(shellToolError(result), "shell is not running") {
			t.Error(shellToolError(result))
		}
	})
	marker := "ASH_STATE_" + strings.TrimPrefix(id, "sh_")
	call(first, "ash_shell_send", map[string]any{"host": name, "shell_id": id, "input": "ASH_PERSIST='" + marker + "'; export ASH_PERSIST\n"})
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("first ASH process disconnected; reconnecting")
	second := connect()
	defer second.Close()
	list := call(second, "ash_shell_list", map[string]any{"host": name})
	data, _ := json.Marshal(list)
	if !strings.Contains(string(data), id) {
		t.Fatalf("shell missing after reconnect: %s", data)
	}
	call(second, "ash_shell_send", map[string]any{"host": name, "shell_id": id, "input": "printf '\\nRESULT:%s:END\\n' \"$ASH_PERSIST\"\n"})
	deadline := time.Now().Add(20 * time.Second)
	for {
		result := call(second, "ash_shell_read", map[string]any{"host": name, "shell_id": id})
		text, _ := result["content"].(string)
		if strings.Contains(text, "RESULT:"+marker+":END") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("persistent environment missing in snapshot: %q", text)
		}
		time.Sleep(100 * time.Millisecond)
	}
	call(second, "ash_shell_close", map[string]any{"host": name, "shell_id": id})
	list = call(second, "ash_shell_list", map[string]any{"host": name})
	data, _ = json.Marshal(list)
	if strings.Contains(string(data), id) {
		t.Fatalf("closed shell still listed: %s", data)
	}
	call(second, "ash_shell_close", map[string]any{"host": name, "shell_id": id})
	closed = true
	t.Log("persistent shell state, output, listing and idempotent cleanup verified across MCP processes")
}

func shellToolError(result *sdk.CallToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		if text, ok := content.(*sdk.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "; ")
}
