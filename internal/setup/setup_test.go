package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func baseOpts(t *testing.T, agent string, scope Scope, projectDir string) Options {
	t.Helper()
	return Options{
		Agent:      agent,
		Scope:      scope,
		ProjectDir: projectDir,
		AshPath:    "/usr/local/bin/ash",
		ConfigPath: "/home/user/.config/ash/config.toml",
	}
}

func TestCodexSetupPreservesUnrelatedAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "model = \"gpt-5\"\n\n[mcp_servers.other]\ncommand = \"other\"\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Run(baseOpts(t, "codex", UserScope, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Written || result.Path != path {
		t.Fatalf("%+v", result)
	}
	content := result.Content
	for _, want := range []string{`model = "gpt-5"`, "[mcp_servers.other]", "[mcp_servers.ash]", "/usr/local/bin/ash", "--config", "/home/user/.config/ash/config.toml", "mcp"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %q in:\n%s", want, content)
		}
	}
	if strings.Count(content, "[mcp_servers.ash]") != 1 {
		t.Fatalf("duplicate ASH entry:\n%s", content)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil || string(onDisk) != content {
		t.Fatalf("on-disk mismatch: %v", err)
	}
	second, err := Run(baseOpts(t, "codex", UserScope, ""))
	if err != nil {
		t.Fatal(err)
	}
	if second.Written || second.Content != content {
		t.Fatalf("setup was not idempotent: %+v", second)
	}
}

func TestCodexSetupUpdatesExistingEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "[mcp_servers.ash]\ncommand = \"old\"\nargs = []\n\n[other]\nx = 1\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Run(baseOpts(t, "codex", UserScope, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Written || strings.Contains(result.Content, `"old"`) || !strings.Contains(result.Content, "[other]\nx = 1") {
		t.Fatalf("bad update:\n%s", result.Content)
	}
	if strings.Count(result.Content, "[mcp_servers.ash]") != 1 {
		t.Fatalf("duplicate ASH entry:\n%s", result.Content)
	}
}

func TestOpenCodeSetupPreservesOtherKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	original := `{"theme":"dark","mcp":{"existing":{"type":"local","command":["x"]}}}`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Run(baseOpts(t, "opencode", UserScope, ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"theme": "dark"`, `"existing"`, `"ash"`, `"type": "local"`, `"enabled": true`, `https://opencode.ai/config.json`, `"/usr/local/bin/ash"`} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("missing %q in:\n%s", want, result.Content)
		}
	}
	second, err := Run(baseOpts(t, "opencode", UserScope, ""))
	if err != nil || second.Written || second.Content != result.Content {
		t.Fatalf("not idempotent: %+v %v", second, err)
	}
}

func TestFreebuffProjectSetupAndScopeRestriction(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".agents", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{"mcpServers":{"other":{"command":"x","args":[]}}}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := Run(baseOpts(t, "freebuff", ProjectScope, project))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, `"other"`) || !strings.Contains(result.Content, `"mcpServers"`) || !strings.Contains(result.Content, `"ash"`) {
		t.Fatalf("bad content:\n%s", result.Content)
	}
	if _, err := Run(baseOpts(t, "freebuff", UserScope, project)); err == nil {
		t.Fatal("accepted unsupported freebuff user scope")
	}
}

func TestPrintModeDoesNotWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	opts := baseOpts(t, "codex", UserScope, "")
	opts.Print = true
	result, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Written || result.Content == "" {
		t.Fatalf("%+v", result)
	}
	if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
		t.Fatalf("print mode wrote a file: %v", err)
	}
}

func TestUnsupportedAgentAndManualExample(t *testing.T) {
	_, err := Run(baseOpts(t, "claude", UserScope, ""))
	if err == nil || !strings.Contains(err.Error(), "Manual configuration example") || !strings.Contains(err.Error(), "mcpServers") {
		t.Fatalf("%v", err)
	}
}
