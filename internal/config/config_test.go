package config

import "testing"

func TestPolicyValidationAndAuditLog(t *testing.T) {
	for _, s := range []string{
		"[hosts.x]\naddress='a'\nuser='b'\n[hosts.x.policy]\nread=true\nread_roots=['relative']",
		"[hosts.x]\naddress='a'\nuser='b'\n[hosts.x.policy]\nexec=true\nmax_timeout_seconds=9999",
		"[hosts.x]\naddress='a'\nuser='b'\n[hosts.x.policy]\nexec=true\nallowed_commands=['']",
	} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Fatalf("accepted invalid policy %s", s)
		}
	}
	c, err := Parse([]byte("[hosts.x]\naddress='a'\nuser='b'\n[hosts.x.policy]\nexec=true\nread_roots=['/var/log']\nmax_timeout_seconds=30\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Hosts["x"].Policy.ReadRoots) != 1 || c.Hosts["x"].Policy.MaxTimeoutSeconds != 30 {
		t.Fatalf("%+v", c.Hosts["x"].Policy)
	}
	c, err = Parse([]byte("audit_log='/tmp/ash-audit.jsonl'\n[hosts.x]\naddress='a'\nuser='b'\n"))
	if err != nil || c.AuditLog != "/tmp/ash-audit.jsonl" {
		t.Fatalf("%q %v", c.AuditLog, err)
	}
}

func TestShellBackendValidation(t *testing.T) {
	c, err := Parse([]byte("[hosts.x]\naddress='a'\nuser='b'\nshell_backend='tmux'\n"))
	if err != nil || c.Hosts["x"].ShellBackend != "tmux" {
		t.Fatalf("%+v %v", c.Hosts["x"], err)
	}
	if _, err := Parse([]byte("[hosts.x]\naddress='a'\nuser='b'\nshell_backend='screen'\n")); err == nil {
		t.Fatal("accepted unknown shell_backend")
	}
}

func TestParseDefaultsAndValidation(t *testing.T) {
	c, err := Parse([]byte("[hosts.fedora]\naddress='100.64.1.20'\nuser='ata'\n"))
	if err != nil {
		t.Fatal(err)
	}
	h := c.Hosts["fedora"]
	if h.Port != 22 || h.Policy.Exec || h.Policy.Read || h.Policy.Write {
		t.Fatalf("unsafe defaults: %+v", h)
	}
	for _, s := range []string{"[hosts.x]\nuser='a'", "[hosts.x]\naddress='a'\nuser='b'\nport=70000", "[hosts.x]\naddress='a'\nuser='b'\n[hosts.x.policy]\nraed=true"} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Fatalf("accepted invalid config %s", s)
		}
	}
}

func TestParseSSHAliasHosts(t *testing.T) {
	c, err := Parse([]byte("[hosts.a]\nssh_alias='fedora'\n[hosts.a.policy]\nexec=true\n"))
	if err != nil {
		t.Fatal(err)
	}
	h := c.Hosts["a"]
	if h.SSHAlias != "fedora" || h.Address != "" || h.User != "" || h.Port != 0 || !h.Policy.Exec {
		t.Fatalf("%+v", h)
	}
	// An alias may coexist with explicit overrides; port stays unset so the
	// resolved OpenSSH value can apply.
	if c, err = Parse([]byte("[hosts.b]\nssh_alias='x'\naddress='1.2.3.4'\nuser='u'\n")); err != nil || c.Hosts["b"].Port != 0 {
		t.Fatalf("%v %+v", err, c.Hosts["b"])
	}
}
