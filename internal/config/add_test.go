package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ash/internal/host"
	"ash/internal/policy"
)

func TestAddHostCreatesAndParses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	written, err := AddHost(path, "fedora", host.Host{Address: "100.64.1.20", Port: 2222, User: "ata", Identity: "~/keys/id", Policy: policy.Policy{Exec: true}})
	if err != nil || !written {
		t.Fatalf("%v %v", written, err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := c.Hosts["fedora"]
	if !ok || h.Address != "100.64.1.20" || h.Port != 2222 || h.User != "ata" || !h.Policy.Exec || h.Policy.Read {
		t.Fatalf("%+v", h)
	}
}

func TestAddHostAppendsPreservingExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "[hosts.first]\naddress = 'a'\nuser = 'u'\n\n[hosts.first.policy]\nexec = true\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AddHost(path, "second", host.Host{Address: "b", Port: 22, User: "u"}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Hosts) != 2 || !c.Hosts["first"].Policy.Exec || c.Hosts["second"].Address != "b" {
		t.Fatalf("%+v", c.Hosts)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "[hosts.first]") {
		t.Fatalf("existing host lost:\n%s", data)
	}
}

func TestAddHostRefusesDuplicatesAndInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := AddHost(path, "h", host.Host{Address: "a", Port: 22, User: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddHost(path, "h", host.Host{Address: "b", Port: 22, User: "u"}); err == nil {
		t.Fatal("accepted duplicate host")
	}
	if _, err := AddHost(path, "bad name", host.Host{Address: "a", Port: 22, User: "u"}); err == nil {
		t.Fatal("accepted invalid host name")
	}
	invalid := filepath.Join(t.TempDir(), "invalid.toml")
	if err := os.WriteFile(invalid, []byte("this is not toml = ="), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(invalid)
	if _, err := AddHost(invalid, "h", host.Host{Address: "a", Port: 22, User: "u"}); err == nil {
		t.Fatal("modified invalid config")
	}
	after, _ := os.ReadFile(invalid)
	if string(before) != string(after) {
		t.Fatal("invalid config was modified")
	}
}
