package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"ash/internal/host"
)

type Config struct {
	Hosts map[string]host.Host `toml:"hosts"`
	// AuditLog, when set, records structured operation metadata to this local
	// owner-only JSONL file.
	AuditLog string `toml:"audit_log"`
}

func Parse(data []byte) (Config, error) {
	var c Config
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	for name, h := range c.Hosts {
		if strings.TrimSpace(name) == "" {
			return c, fmt.Errorf("host %q requires a name", name)
		}
		alias := strings.TrimSpace(h.SSHAlias)
		if strings.ContainsRune(h.SSHAlias, 0) {
			return c, fmt.Errorf("host %q: ssh_alias must not contain NUL", name)
		}
		if alias == "" && strings.TrimSpace(h.Address) == "" {
			return c, fmt.Errorf("host %q requires an address or an ssh_alias", name)
		}
		if alias == "" && strings.TrimSpace(h.User) == "" {
			return c, fmt.Errorf("host %q requires a user or an ssh_alias", name)
		}
		if h.Port == 0 && alias == "" {
			h.Port = 22
		}
		if h.Port != 0 && (h.Port < 1 || h.Port > 65535) {
			return c, fmt.Errorf("host %q: invalid port %d", name, h.Port)
		}
		if err := h.Policy.Validate(); err != nil {
			return c, fmt.Errorf("host %q: %w", name, err)
		}
		switch h.ShellBackend {
		case "", "zellij", "tmux":
		default:
			return c, fmt.Errorf("host %q: shell_backend must be zellij or tmux", name)
		}
		h.Name = name
		c.Hosts[name] = h
	}
	if strings.ContainsRune(c.AuditLog, 0) {
		return c, fmt.Errorf("audit_log must not contain NUL")
	}
	return c, nil
}
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ash", "config.toml"), nil
}
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("load config %q: %w", path, err)
	}
	return Parse(data)
}
