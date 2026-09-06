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
}

func Parse(data []byte) (Config, error) {
	var c Config
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	for name, h := range c.Hosts {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(h.Address) == "" || strings.TrimSpace(h.User) == "" {
			return c, fmt.Errorf("host %q requires a name, address and user", name)
		}
		if h.Port == 0 {
			h.Port = 22
		}
		if h.Port < 1 || h.Port > 65535 {
			return c, fmt.Errorf("host %q: invalid port %d", name, h.Port)
		}
		h.Name = name
		c.Hosts[name] = h
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
