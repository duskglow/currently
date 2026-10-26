package config

import (
	"strings"
	"testing"

	"github.com/nut-ng/nut-ng/internal/registry"
)

func TestSNMPConfig(t *testing.T) {
	t.Setenv("NUTNG_TEST_SNMP_PRIV", "privpassword")
	src := `
ups:
  rack: {driver: simulator}
listen:
  snmp:
    community: s3cret
    ups: rack
    users:
      monitor:
        auth: SHA-256
        auth_password: authpassword
        privacy: aes128
        privacy_password_env: NUTNG_TEST_SNMP_PRIV
    traps:
      - target: 192.0.2.1
      - target: "[2001:db8::1]:1162"
        community: traps
`
	c, err := Parse("nutng.yaml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(registry.Builtin()); err != nil {
		t.Fatal(err)
	}
	s := c.Listen.SNMP
	if s.Addr != DefaultSNMP || s.Secret() != "s3cret" {
		t.Errorf("snmp = %+v", s)
	}
	if _, priv := s.Users["monitor"].Secrets(); priv != "privpassword" {
		t.Errorf("privacy password %q", priv)
	}
	if TrapAddr(s.Traps[0].Target) != "192.0.2.1:162" || TrapAddr(s.Traps[1].Target) != "[2001:db8::1]:1162" {
		t.Errorf("trap addresses %s %s", TrapAddr(s.Traps[0].Target), TrapAddr(s.Traps[1].Target))
	}
	out, _ := c.YAML()
	if strings.Contains(string(out), "s3cret") || strings.Contains(string(out), "authpassword") {
		t.Errorf("secrets visible:\n%s", out)
	}

	for src, want := range map[string]string{
		"listen:\n  snmp: \"127.0.0.1:1161\"\n":  "127.0.0.1:1161",
		"listen:\n  snmp: {}\n":                  DefaultSNMP,
		"listen:\n  snmp: off\n":                 "",
		"listen:\n  http: off\n  metrics: off\n": "",
	} {
		c, err := Parse("nutng.yaml", []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if c.Listen.SNMP != nil {
			got = c.Listen.SNMP.Addr
		}
		if got != want {
			t.Errorf("%q: addr %q, want %q", src, got, want)
		}
	}
}

func TestSNMPConfigErrors(t *testing.T) {
	user := func(fields string) string {
		return "listen:\n  snmp:\n    users:\n      u:\n" + fields
	}
	tests := []struct {
		name, src string
		want      []string
	}{
		{"key typo", "listen:\n  snmp:\n    comunity: x\n", []string{"nutng.yaml:3:", `did you mean "community"`}},
		{"user key typo", user("        auth_pasword: x\n"), []string{"nutng.yaml:5:", `did you mean "auth_password"`}},
		{"bad addr", "listen:\n  snmp: 1161\n", []string{"listen.snmp.addr"}},
		{"unknown ups", "listen:\n  snmp:\n    ups: nope\n", []string{"listen.snmp.ups", `"nope"`}},
		{"bad auth", user("        auth: sha3\n        auth_password: authpassword\n"), []string{"auth \"sha3\" is not one of"}},
		{"short password", user("        auth: sha\n        auth_password: short\n"), []string{"at least 8 characters"}},
		{"no password", user("        auth: sha\n"), []string{"set auth_password or auth_password_env"}},
		{"des", user("        auth: sha\n        auth_password: authpassword\n        privacy: des\n        privacy_password: privpassword\n"),
			[]string{"DES is insecure"}},
		{"privacy without auth", user("        auth: none\n        privacy: aes\n        privacy_password: privpassword\n"),
			[]string{"privacy needs an auth protocol"}},
		{"bad engine id", "listen:\n  snmp:\n    engine_id: xyz\n", []string{"engine_id"}},
		{"bad trap", "listen:\n  snmp:\n    traps:\n      - community: x\n", []string{"traps[0]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse("nutng.yaml", []byte(tt.src))
			if err == nil {
				err = c.Validate(registry.Builtin())
			}
			if err == nil {
				t.Fatal("no error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}
