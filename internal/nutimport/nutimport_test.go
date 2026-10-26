package nutimport

import (
	"strings"
	"testing"
	"time"

	"github.com/duskglow/currently/internal/config"
)

func convert(t *testing.T, dir string) (*Result, *config.Config) {
	t.Helper()
	res, err := Convert(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Parse("currently.yaml", []byte(res.YAML))
	if err != nil {
		t.Fatalf("%v\n%s", err, res.YAML)
	}
	if err := c.Validate(Registry()); err != nil {
		t.Fatalf("%v\n%s", err, res.YAML)
	}
	return res, c
}

func TestPrimary(t *testing.T) {
	res, c := convert(t, "testdata/primary")

	rack, ok := c.UPS["rack"]
	if !ok || len(c.UPS) != 4 {
		t.Fatalf("UPSes = %v\n%s", c.UPSNames(), res.YAML)
	}
	if rack.Driver != "usbhid" || rack.Description != "Rack UPS (APC)" || rack.Options["id"] != "051d:0002" ||
		rack.Options["serial"] != "3B1234X56789" || rack.Options["interval"] != "5s" {
		t.Errorf("rack = %+v", rack)
	}
	for name, want := range map[string]map[string]any{
		"closet": {"driver": "snmp", "host": "192.168.1.20", "community": "public", "mib": "apc", "interval": "15s"},
		"serial": {"driver": "serial", "port": "/dev/ttyS0", "protocol": "megatec", "battery_voltage_low": 21.0,
			"battery_voltage_high": 26.0, "description": "Basement UPS"},
		"apc": {"driver": "serial", "port": "/dev/ttyS1", "protocol": "apcsmart"},
	} {
		u := c.UPS[name]
		for k, v := range want {
			got := u.Options[k]
			switch k {
			case "driver":
				got = u.Driver
			case "description":
				got = u.Description
			}
			if got != v {
				t.Errorf("%s.%s = %v (%T), want %v\n%s", name, k, got, got, v, res.YAML)
			}
		}
	}
	if c.Listen.NUT != "127.0.0.1:3493" {
		t.Errorf("listen.nut = %q", c.Listen.NUT)
	}
	wantUsers := map[string]string{"admin": "admin", "upsmon": "primary", "monslave": "secondary"}
	if len(c.Users) != len(wantUsers) {
		t.Errorf("users = %+v", c.Users)
	}
	for name, role := range wantUsers {
		if c.Users[name].Role != role {
			t.Errorf("user %s role %q, want %q", name, c.Users[name].Role, role)
		}
	}
	if c.Users["upsmon"].Password != "mon pass" {
		t.Errorf("quoted password lost: %q", c.Users["upsmon"].Password)
	}

	sd := c.Shutdown
	if sd == nil {
		t.Fatalf("no shutdown section:\n%s", res.YAML)
	}
	if sd.Command != "" {
		t.Errorf("the standard shutdown command must map to the built-in one, got %q", sd.Command)
	}
	if sd.WaitForSecondaries != 20*time.Second || sd.DeadTime != 25*time.Second || sd.FinalDelay != 5*time.Second || sd.MinSupplies != 1 {
		t.Errorf("shutdown = %+v", sd)
	}
	if sd.PowerOffUPS {
		t.Error("power_off_ups must not be turned on by an import")
	}

	notes := strings.Join(res.Notes, "\n")
	for _, want := range []string{"[usbq]: skipped: driver nutdrv_qx on USB", "[old]: skipped: driver bcmxcp", "[test]: skipped: dummy-ups", "[reader]: skipped",
		"POWERDOWNFLAG", "NOTIFYCMD", "listened on 127.0.0.1:3493, 192.168.1.10:3493"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
}

func TestSecondary(t *testing.T) {
	res, c := convert(t, "testdata/secondary")
	rack, office := c.UPS["rack"], c.UPS["office"]
	if rack.Driver != "nut" || rack.Options["host"] != "nas.local:3493" || rack.Options["username"] != "monslave" || rack.Options["password"] != "slavepw" {
		t.Errorf("rack = %+v\n%s", rack, res.YAML)
	}
	if office.Driver != "nut" || office.Options["host"] != "10.0.0.7" {
		t.Errorf("office = %+v", office)
	}
	if got := c.Supplies(); len(got) != 1 || got[0] != "rack" {
		t.Errorf("supplies = %v (office has power value 0)", got)
	}
	if c.Shutdown.Command != "/usr/local/bin/graceful-off" {
		t.Errorf("custom SHUTDOWNCMD = %q", c.Shutdown.Command)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "power value 0") {
		t.Errorf("notes: %v", res.Notes)
	}
}

func TestNotANUTDirectory(t *testing.T) {
	if _, err := Convert(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no ups.conf or upsmon.conf") {
		t.Errorf("got %v", err)
	}
}
