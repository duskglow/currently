package config

import (
	"strings"
	"testing"
	"time"

	"github.com/nut-ng/nut-ng/internal/registry"
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
	c, err := Parse("nutng.yaml", []byte(src))
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
	c, err := Parse("nutng.yaml", []byte("# nothing yet\n"))
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
		{"unknown top-level key", "lisen:\n  http: off\n", []string{"nutng.yaml:1:", `did you mean "listen"`}},
		{"unknown nested key", "log:\n  levle: info\n", []string{"nutng.yaml:2:", `did you mean "level"`}},
		{"bad yaml", "ups:\n  rack: [\n", []string{"nutng.yaml:"}},
		{"wrong type", "ups:\n  rack:\n    stale_after: soon\n", []string{"nutng.yaml:3:"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("nutng.yaml", []byte(tt.src))
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
		{"unknown driver", "ups:\n  rack:\n    driver: simulater\n", []string{"nutng.yaml:3:", `did you mean "simulator"`}},
		{"missing driver", "ups:\n  rack:\n    description: x\n", []string{"nutng.yaml:2:", "no driver set"}},
		{"driver option typo", "ups:\n  rack:\n    driver: simulator\n    scenaro: cycle\n", []string{"nutng.yaml:4:", `did you mean "scenario"`}},
		{"driver option value", "ups:\n  rack:\n    driver: simulator\n    load: 150\n", []string{"nutng.yaml:4:", "load must be between 0 and 100"}},
		{"bad name", "ups:\n  my ups:\n    driver: simulator\n", []string{"may only contain"}},
		{"bad listen", "listen:\n  http: localhost\n", []string{"nutng.yaml:2:", "host:port"}},
		{"bad level", "log:\n  level: verbose\n", []string{"nutng.yaml:2:", "log.level"}},
		{"missing token env", "api:\n  token_env: NUTNG_TEST_SURELY_UNSET\n", []string{"NUTNG_TEST_SURELY_UNSET", "not set"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse("nutng.yaml", []byte(tt.src))
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

func TestHTTPOff(t *testing.T) {
	c, err := Parse("nutng.yaml", []byte("listen:\n  http: off\n"))
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
