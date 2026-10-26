package main

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"github.com/nut-ng/nut-ng/internal/config"
	"github.com/nut-ng/nut-ng/internal/registry"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

func prompterFor(input string) *prompter {
	return &prompter{in: bufio.NewReader(strings.NewReader(input)), out: io.Discard}
}

func TestPrompter(t *testing.T) {
	if !prompterFor("\n").yesNo("q", true) || prompterFor("\n").yesNo("q", false) {
		t.Error("yesNo default")
	}
	if !prompterFor("maybe\nyes\n").yesNo("q", false) {
		t.Error("yesNo should re-ask until it gets an answer")
	}

	list := []found{{UPS: "a"}, {UPS: "b"}, {UPS: "c"}}
	if got := prompterFor("\n").choose("q", list); len(got) != 3 {
		t.Errorf("Enter should choose all, got %d", len(got))
	}
	got := prompterFor("9\n3, 1\n").choose("q", list)
	if len(got) != 2 || got[0].UPS != "c" || got[1].UPS != "a" {
		t.Errorf("choose = %+v", got)
	}

	taken := map[string]bool{"rack": true}
	if n := prompterFor("bad name\nrack\nups-1\n").name("q", "ups", taken); n != "ups-1" {
		t.Errorf("name = %q", n)
	}
	if n := prompterFor("\n").name("q", "rack", taken); n != "rack2" {
		t.Errorf("default name should avoid taken names, got %q", n)
	}
}

func TestRenderConfigIsValid(t *testing.T) {
	ups := found{UPS: "cyberpower", Discovered: sdk.Discovered{Driver: "usbhid", Name: `CPS "CP1500"`,
		Config: sdk.Config{"id": "0764:0601", "serial": "X1"}}}
	for _, tc := range []struct {
		chosen        []found
		demo, network bool
	}{
		{[]found{ups}, false, false},
		{[]found{ups}, false, true},
		{nil, true, false},
	} {
		text := renderConfig(tc.chosen, tc.demo, tc.network)
		c, err := config.Parse("nutng.yaml", []byte(text))
		if err != nil {
			t.Fatalf("%v\n%s", err, text)
		}
		if err := c.Validate(registry.Builtin()); err != nil {
			t.Fatalf("%v\n%s", err, text)
		}
		if len(tc.chosen) == 1 {
			u := c.UPS["cyberpower"]
			if u.Options["serial"] != "X1" || u.Options["id"] != nil || u.Description != `CPS "CP1500"` {
				t.Errorf("ups block = %+v", u)
			}
		}
		if tc.network && c.Listen.HTTP != ":9300" {
			t.Errorf("network listen = %q", c.Listen.HTTP)
		}
	}
}

func TestSuggestName(t *testing.T) {
	taken := map[string]bool{}
	apc := sdk.Discovered{Config: sdk.Config{"id": "051d:0002"}}
	if a, b := uniqueName(suggestName(apc), taken), uniqueName(suggestName(apc), taken); a != "apc" || b != "apc2" {
		t.Errorf("names %s, %s", a, b)
	}
	if n := suggestName(sdk.Discovered{Config: sdk.Config{"id": "abcd:0001"}}); n != "ups" {
		t.Errorf("unknown vendor name %s", n)
	}
}
