package prometheus

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/duskglow/currently/pkg/sdk"
)

func sample() []sdk.UPS {
	vars := func(kv ...string) map[string]sdk.Variable {
		m := map[string]sdk.Variable{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = sdk.Variable{Name: kv[i], Value: kv[i+1]}
		}
		return m
	}
	return []sdk.UPS{
		{Name: "rack", Driver: "usbhid", State: sdk.StateConnected, LastUpdate: time.Unix(1700000000, 500e6),
			Description: `Rack "A"`,
			Vars: vars("ups.status", "OB DISCHRG LB", "battery.charge", "42", "battery.runtime", "600",
				"input.voltage", "0.0", "ups.power", "379", "ups.temperature", "31.5",
				"device.mfr", "CPS", "device.model", "CP1500", "device.serial", "X1", "ups.beeper.status", "enabled")},
		{Name: "closet", Driver: "usbhid", State: sdk.StateStale, Restarts: 3,
			Vars: vars("battery.charge", "100")},
	}
}

var (
	helpRE   = regexp.MustCompile(`^# HELP [a-zA-Z_:][a-zA-Z0-9_:]* .+$`)
	typeRE   = regexp.MustCompile(`^# TYPE [a-zA-Z_:][a-zA-Z0-9_:]* (gauge|counter)$`)
	sampleRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{([a-zA-Z_][a-zA-Z0-9_]*="([^"\\]|\\.)*",?)*\})? -?[0-9.e+-]+$`)
)

func TestWrite(t *testing.T) {
	var b bytes.Buffer
	Write(&b, sample(), "v1.2.3")
	out := b.String()

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !helpRE.MatchString(line) && !typeRE.MatchString(line) && !sampleRE.MatchString(line) {
			t.Errorf("invalid exposition line: %q", line)
		}
	}
	for _, want := range []string{
		`currently_build_info{version="v1.2.3"} 1`,
		`currently_ups_up{ups="rack"} 1`,
		`currently_ups_up{ups="closet"} 0`,
		`currently_battery_charge_percent{ups="rack"} 42`,
		`currently_battery_runtime_seconds{ups="rack"} 600`,
		`currently_input_voltage_volts{ups="rack"} 0`,
		`currently_ups_power_voltamperes{ups="rack"} 379`,
		`currently_ups_temperature_celsius{ups="rack"} 31.5`,
		`currently_ups_status{flag="OB",ups="rack"} 1`,
		`currently_ups_status{flag="OL",ups="rack"} 0`,
		`currently_ups_status{flag="LB",ups="rack"} 1`,
		`currently_ups_driver_restarts_total{ups="closet"} 3`,
		`currently_ups_last_update_timestamp_seconds{ups="rack"} 1.7000000005e+09`,
		`description="Rack \"A\""`,
		"# TYPE currently_ups_driver_restarts_total counter",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s", want)
		}
	}
	// A stale UPS must not export its last values as current.
	if strings.Contains(out, `currently_battery_charge_percent{ups="closet"}`) {
		t.Error("stale UPS exported values")
	}
	// Each metric family appears once, with HELP and TYPE.
	if n := strings.Count(out, "# TYPE currently_ups_status "); n != 1 {
		t.Errorf("currently_ups_status TYPE lines: %d", n)
	}
	if strings.Contains(out, "beeper") {
		t.Error("non-numeric variable exported")
	}
}
