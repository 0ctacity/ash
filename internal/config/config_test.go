package config

import "testing"

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
