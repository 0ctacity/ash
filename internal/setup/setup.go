// Package setup registers ASH as a stdio MCP server with supported coding agents.
//
// It writes only the target agent's documented MCP configuration file, preserves
// unrelated entries, and is idempotent: re-running setup never creates a
// duplicate ASH entry.
package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Scope string

const (
	UserScope    Scope = "user"
	ProjectScope Scope = "project"
)

// Adapter describes one supported coding agent and where it stores MCP servers.
type Adapter struct {
	Name string
	// Scopes lists the configuration scopes the agent supports.
	Scopes []Scope
	// Path resolves the configuration file for a scope.
	Path func(scope Scope, projectDir string) (string, error)
	// Render produces the full configuration file from its current contents.
	Render func(existing []byte, command []string) ([]byte, error)
	// Restart tells the user how to pick up the change.
	Restart string
}

// Options controls one setup invocation.
type Options struct {
	Agent      string
	Scope      Scope
	ProjectDir string
	AshPath    string
	ConfigPath string
	Print      bool
}

// Result reports what setup did and the exact configuration it produced.
type Result struct {
	Agent   string   `json:"agent"`
	Scope   Scope    `json:"scope"`
	Path    string   `json:"path"`
	Command []string `json:"command"`
	Written bool     `json:"written"`
	Content string   `json:"content"`
	Restart string   `json:"restart"`
}

func adapters() []Adapter {
	return []Adapter{
		{
			Name:    "codex",
			Scopes:  []Scope{UserScope, ProjectScope},
			Path:    codexPath,
			Render:  renderCodex,
			Restart: "Restart Codex (or start a new session) so it reloads config.toml.",
		},
		{
			Name:    "opencode",
			Scopes:  []Scope{UserScope, ProjectScope},
			Path:    opencodePath,
			Render:  renderOpenCode,
			Restart: "Restart OpenCode so it reloads its config and starts the ASH MCP server.",
		},
		{
			Name:    "freebuff",
			Scopes:  []Scope{ProjectScope},
			Path:    freebuffPath,
			Render:  renderFreebuff,
			Restart: "Restart freebuff from the project directory so it reloads .agents/mcp.json.",
		},
	}
}

// Agents returns the supported adapters sorted by name.
func Agents() []Adapter {
	out := adapters()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns the adapter for an agent name.
func Lookup(name string) (Adapter, bool) {
	for _, a := range adapters() {
		if a.Name == name {
			return a, true
		}
	}
	return Adapter{}, false
}

// ManualExample returns the generic stdio configuration for unsupported agents.
func ManualExample(command []string) string {
	entry := map[string]any{"mcpServers": map[string]any{"ash": map[string]any{
		"command": command[0],
		"args":    command[1:],
	}}}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return ""
	}
	return string(data) + "\n"
}

// Run renders (and unless Print is set, writes) the ASH MCP entry for one agent.
func Run(opts Options) (Result, error) {
	adapter, ok := Lookup(opts.Agent)
	if !ok {
		names := make([]string, 0)
		for _, a := range Agents() {
			names = append(names, a.Name)
		}
		cmd, err := command(nil, opts)
		if err != nil {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("unsupported agent %q; supported: %s\n\nManual configuration example:\n%s", opts.Agent, strings.Join(names, ", "), ManualExample(cmd))
	}
	scope := opts.Scope
	if scope == "" {
		if len(adapter.Scopes) > 0 {
			scope = adapter.Scopes[0]
		}
	}
	if !supports(adapter.Scopes, scope) {
		return Result{}, fmt.Errorf("%s does not support %s scope; supported scopes: %s", adapter.Name, scope, scopeList(adapter.Scopes))
	}
	projectDir := opts.ProjectDir
	if projectDir == "" {
		dir, err := os.Getwd()
		if err != nil {
			return Result{}, err
		}
		projectDir = dir
	}
	cmd, err := command(&adapter, opts)
	if err != nil {
		return Result{}, err
	}
	path, err := adapter.Path(scope, projectDir)
	if err != nil {
		return Result{}, err
	}
	existing, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return Result{}, fmt.Errorf("read %s: %w", path, readErr)
	}
	content, err := adapter.Render(existing, cmd)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", adapter.Name, err)
	}
	result := Result{Agent: adapter.Name, Scope: scope, Path: path, Command: cmd, Content: string(content), Restart: adapter.Restart}
	if opts.Print {
		return result, nil
	}
	if errors.Is(readErr, fs.ErrNotExist) || !bytes.Equal(existing, content) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return Result{}, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", path, err)
		}
		result.Written = true
	}
	return result, nil
}

func command(adapter *Adapter, opts Options) ([]string, error) {
	ashPath := opts.AshPath
	if ashPath == "" {
		var err error
		ashPath, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve ASH executable: %w", err)
		}
	}
	ashPath, err := filepath.Abs(ashPath)
	if err != nil {
		return nil, fmt.Errorf("resolve ASH executable: %w", err)
	}
	configPath := opts.ConfigPath
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		configPath = filepath.Join(home, ".config", "ash", "config.toml")
	}
	return []string{ashPath, "--config", configPath, "mcp"}, nil
}

func supports(scopes []Scope, scope Scope) bool {
	for _, s := range scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func scopeList(scopes []Scope) string {
	names := make([]string, 0, len(scopes))
	for _, s := range scopes {
		names = append(names, string(s))
	}
	return strings.Join(names, ", ")
}

func codexPath(scope Scope, projectDir string) (string, error) {
	if scope == ProjectScope {
		return filepath.Join(projectDir, ".codex", "config.toml"), nil
	}
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Join(home, "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

func opencodePath(scope Scope, projectDir string) (string, error) {
	if scope == ProjectScope {
		return filepath.Join(projectDir, "opencode.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode", "opencode.json"), nil
}

func freebuffPath(scope Scope, projectDir string) (string, error) {
	if scope != ProjectScope {
		return "", fmt.Errorf("freebuff only documents project-level setup; use --scope project")
	}
	return filepath.Join(projectDir, ".agents", "mcp.json"), nil
}

func renderCodex(existing []byte, command []string) ([]byte, error) {
	block, err := toml.Marshal(struct {
		Command string   `toml:"command"`
		Args    []string `toml:"args"`
	}{Command: command[0], Args: command[1:]})
	if err != nil {
		return nil, err
	}
	header := "[mcp_servers.ash]"
	section := header + "\n" + string(block)
	return []byte(upsertTOMLSection(string(existing), header, section)), nil
}

// upsertTOMLSection replaces the table whose header is exactly `header`, or
// appends it. It preserves every other line of the document, including comments.
func upsertTOMLSection(doc, header, section string) string {
	doc = strings.TrimRight(doc, "\n")
	lines := strings.Split(doc, "\n")
	sectionLines := strings.Split(strings.TrimRight(section, "\n"), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			start = i
			break
		}
	}
	var out []string
	if start == -1 {
		if strings.TrimSpace(doc) != "" {
			out = append(out, lines...)
			out = append(out, "")
		}
		out = append(out, sectionLines...)
	} else {
		end := len(lines)
		for i := start + 1; i < len(lines); i++ {
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
				end = i
				break
			}
		}
		out = append(out, lines[:start]...)
		out = append(out, sectionLines...)
		out = append(out, lines[end:]...)
	}
	return strings.Join(out, "\n") + "\n"
}

func renderOpenCode(existing []byte, command []string) ([]byte, error) {
	entry := map[string]any{"type": "local", "command": command, "enabled": true}
	return upsertJSONEntry(existing, "mcp", "ash", entry, "https://opencode.ai/config.json")
}

func renderFreebuff(existing []byte, command []string) ([]byte, error) {
	entry := map[string]any{"command": command[0], "args": command[1:]}
	return upsertJSONEntry(existing, "mcpServers", "ash", entry, "")
}

// upsertJSONEntry sets root[container][name] = entry while preserving every
// other key. Invalid JSON is reported rather than overwritten.
func upsertJSONEntry(existing []byte, container, name string, entry map[string]any, schema string) ([]byte, error) {
	root := map[string]any{}
	if trimmed := strings.TrimSpace(string(existing)); trimmed != "" {
		if err := json.Unmarshal(existing, &root); err != nil {
			return nil, fmt.Errorf("parse existing config: %w", err)
		}
	}
	servers, _ := root[container].(map[string]any)
	if raw, ok := root[container]; ok && raw != nil && servers == nil {
		return nil, fmt.Errorf("existing %q is not an object", container)
	}
	if servers == nil {
		servers = map[string]any{}
	}
	servers[name] = entry
	root[container] = servers
	if schema != "" {
		if _, ok := root["$schema"]; !ok {
			root["$schema"] = schema
		}
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
