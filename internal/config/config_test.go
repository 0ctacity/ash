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
