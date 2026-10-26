package config

import (
	"strings"
	"testing"

	"github.com/duskglow/currently/internal/registry"
)

func TestShutdownConfig(t *testing.T) {
	src := `
ups:
  rack: {driver: simulator}
  office: {driver: simulator}
shutdown:
  ups: [rack]
  when:
    on_battery_for: 2m
  hooks: ["echo stop vms"]
`
	c, err := Parse("currently.yaml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(registry.Builtin()); err != nil {
		t.Fatal(err)
	}
	sd := c.Shutdown
	if !*sd.When.LowBattery || sd.MinSupplies != 1 || sd.FinalDelay != DefaultFinalDelay || sd.WaitForSecondaries != DefaultWaitForSecondaries {
		t.Errorf("defaults not filled: %+v", sd)
	}
	if got := c.Supplies(); len(got) != 1 || got[0] != "rack" {
		t.Errorf("supplies = %v", got)
	}
	if Default().Shutdown != nil {
		t.Error("shutdown must be off without a shutdown section")
	}
}

func TestShutdownConfigErrors(t *testing.T) {
	tests := []struct {
		name, src string
		want      []string
	}{
		{"unknown ups", "ups:\n  rack: {driver: simulator}\nshutdown:\n  ups: [rakc]\n", []string{"currently.yaml:4:", `did you mean "rack"`}},
		{"nothing triggers", "ups:\n  rack: {driver: simulator}\nshutdown:\n  when:\n    low_battery: false\n", []string{"nothing would trigger"}},
		{"min supplies", "ups:\n  rack: {driver: simulator}\nshutdown:\n  min_supplies: 2\n", []string{"min_supplies"}},
		{"short ups off delay", "ups:\n  rack: {driver: simulator}\nshutdown:\n  power_off_ups: true\n  ups_off_delay: 2s\n", []string{"at least 10s"}},
		{"key typo", "shutdown:\n  when:\n    on_batery_for: 1m\n", []string{"currently.yaml:3:", `did you mean "on_battery_for"`}},
		{"no ups", "shutdown: {}\n", []string{"no UPS is configured"}},
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
