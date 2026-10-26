package config

import (
	"strings"
	"testing"
	"time"

	"github.com/duskglow/currently/internal/registry"
)

func TestParseValid(t *testing.T) {
	src := `
ups:
  rack:
    driver: simulator
    description: Rack UPS
    scenario: cycle
    speed: 10
  closet:
    driver: simulator
    stale_after: 1m
listen:
  http: ":9300"
log:
  level: debug
`
	c, err := Parse("currently.yaml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(registry.Builtin()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.UPSNames(), ","); got != "rack,closet" {
		t.Errorf("order = %s", got)
	}
	rack := c.UPS["rack"]
	if rack.Description != "Rack UPS" || rack.Options["scenario"] != "cycle" || rack.StaleAfter != DefaultStaleAfter {
		t.Errorf("rack = %+v", rack)
	}
	if _, ok := rack.Options["driver"]; ok {
		t.Error("driver leaked into driver options")
	}
	if c.UPS["closet"].StaleAfter != time.Minute {
		t.Errorf("closet stale_after = %s", c.UPS["closet"].StaleAfter)
	}
	if c.Line("ups.closet.driver") != 9 {
		t.Errorf("line of ups.closet.driver = %d", c.Line("ups.closet.driver"))
	}
}

func TestParseEmptyIsDefault(t *testing.T) {
	c, err := Parse("currently.yaml", []byte("# nothing yet\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen.HTTP != DefaultHTTP || len(c.UPS) != 0 {
		t.Errorf("got %+v", c)
	}
	if err := c.Validate(registry.Builtin()); err != nil {
		t.Error(err)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name, src string
		want      []string
	}{
		{"unknown top-level key", "lisen:\n  http: off\n", []string{"currently.yaml:1:", `did you mean "listen"`}},
		{"unknown nested key", "log:\n  levle: info\n", []string{"currently.yaml:2:", `did you mean "level"`}},
		{"bad yaml", "ups:\n  rack: [\n", []string{"currently.yaml:"}},
		{"wrong type", "ups:\n  rack:\n    stale_after: soon\n", []string{"currently.yaml:3:"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("currently.yaml", []byte(tt.src))
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

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name, src string
		want      []string
	}{
		{"unknown driver", "ups:\n  rack:\n    driver: simulater\n", []string{"currently.yaml:3:", `did you mean "simulator"`}},
		{"usbhid option typo", "ups:\n  rack:\n    serail: X1\n", []string{"currently.yaml:3:", "usbhid driver", `did you mean "serial"`}},
		{"driver option typo", "ups:\n  rack:\n    driver: simulator\n    scenaro: cycle\n", []string{"currently.yaml:4:", `did you mean "scenario"`}},
		{"driver option value", "ups:\n  rack:\n    driver: simulator\n    load: 150\n", []string{"currently.yaml:4:", "load must be between 0 and 100"}},
		{"bad name", "ups:\n  my ups:\n    driver: simulator\n", []string{"may only contain"}},
		{"bad listen", "listen:\n  http: localhost\n", []string{"currently.yaml:2:", "host:port"}},
		{"bad level", "log:\n  level: verbose\n", []string{"currently.yaml:2:", "log.level"}},
		{"missing token env", "api:\n  token_env: CURRENTLY_TEST_SURELY_UNSET\n", []string{"CURRENTLY_TEST_SURELY_UNSET", "not set"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse("currently.yaml", []byte(tt.src))
			if err != nil {
				t.Fatal(err)
			}
			err = c.Validate(registry.Builtin())
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

func TestDefaultDriver(t *testing.T) {
	c, err := Parse("currently.yaml", []byte("ups:\n  rack:\n    serial: X1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(registry.Builtin()); err != nil {
		t.Fatal(err)
	}
	if c.UPS["rack"].Driver != "usbhid" || c.UPS["rack"].Options["serial"] != "X1" {
		t.Errorf("rack = %+v", c.UPS["rack"])
	}
}

func TestHTTPOff(t *testing.T) {
	c, err := Parse("currently.yaml", []byte("listen:\n  http: off\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen.HTTP != "" {
		t.Errorf("http = %q, want disabled", c.Listen.HTTP)
	}
}

func TestYAMLHidesToken(t *testing.T) {
	c := Default()
	c.API.Token = "s3cret"
	out, err := c.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "s3cret") {
		t.Errorf("token visible in:\n%s", out)
	}
}

func TestUsersAndListeners(t *testing.T) {
	t.Setenv("CURRENTLY_TEST_UPSMON_PW", "s3cret")
	src := `
listen:
  nut: ":3493"
  metrics: 127.0.0.1:9301
users:
  upsmon:
    password_env: CURRENTLY_TEST_UPSMON_PW
    role: master
  admin:
    password: hunter2
    role: admin
`
	c, err := Parse("currently.yaml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(registry.Builtin()); err != nil {
		t.Fatal(err)
	}
	if u := c.Users["upsmon"]; u.Role != RolePrimary || u.Secret() != "s3cret" {
		t.Errorf("upsmon = %+v (master must map to primary)", u)
	}
	out, _ := c.YAML()
	if strings.Contains(string(out), "hunter2") {
		t.Errorf("password visible:\n%s", out)
	}
	d := Default()
	if d.Listen.NUT != DefaultNUT {
		t.Errorf("default NUT listener %q", d.Listen.NUT)
	}
}

func TestUsersAndListenerErrors(t *testing.T) {
	tests := []struct {
		name, src string
		want      []string
	}{
		{"bad role", "users:\n  u:\n    password: x\n    role: primray\n", []string{"currently.yaml:4:", `did you mean "primary"`}},
		{"no password", "users:\n  u:\n    role: primary\n", []string{"currently.yaml:2:", "set password or password_env"}},
		{"user key typo", "users:\n  u:\n    pasword: x\n", []string{"currently.yaml:3:", `did you mean "password"`}},
		{"bad nut addr", "listen:\n  nut: 3493\n", []string{"currently.yaml:2:", "listen.nut"}},
		{"metrics without http", "listen:\n  http: off\n", []string{"listen.metrics"}},
		{"missing cert", "listen:\n  nut_tls:\n    cert_file: /nonexistent.pem\n    key_file: /nonexistent.key\n", []string{"cert_file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse("currently.yaml", []byte(tt.src))
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
