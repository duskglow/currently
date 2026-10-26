package usbhid

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/duskglow/currently/drivers/usbhid/hid"
	"github.com/duskglow/currently/drivers/usbhid/hid/hidtest"
	"github.com/duskglow/currently/pkg/sdk"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeBackend serves reports from memory, like a device on hidraw.
type fakeBackend struct {
	mu      sync.Mutex
	reports map[hid.ReportType]map[uint8][]byte
	failAll bool
}

func (b *fakeBackend) GetReport(t hid.ReportType, buf []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failAll {
		return 0, errors.New("device unplugged")
	}
	r, ok := b.reports[t][buf[0]]
	if !ok {
		return 0, errors.New("no such report")
	}
	return copy(buf, r), nil
}

func (b *fakeBackend) SetFeature(buf []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reports[hid.Feature][buf[0]] = append([]byte(nil), buf...)
	return nil
}

func (b *fakeBackend) Close() error { return nil }

var cyberPowerInfo = DeviceInfo{Path: "/dev/hidraw7", VendorID: 0x0764, ProductID: 0x0601,
	Manufacturer: "CPS", Product: "CP1500PFCLCDa", Serial: "CXXTEST0001", Usage: hid.UsageUPS}

// newCyberPower returns a fake CyberPower UPS on a fake backend.
func newCyberPower(t *testing.T, change map[string]int64) (*rawDevice, *fakeBackend) {
	t.Helper()
	desc, values := hidtest.CyberPower()
	for k, v := range change {
		values[k] = v
	}
	d, err := hid.Parse(desc)
	if err != nil {
		t.Fatal(err)
	}
	be := &fakeBackend{reports: map[hid.ReportType]map[uint8][]byte{
		hid.Feature: hidtest.Reports(d, hid.Feature, values),
		hid.Input:   {},
	}}
	dev, err := newRawDevice(cyberPowerInfo, desc, be)
	if err != nil {
		t.Fatal(err)
	}
	return dev, be
}

func mapperFor(t *testing.T, dev Device) *Mapper {
	t.Helper()
	quirks, err := LoadQuirks("")
	if err != nil {
		t.Fatal(err)
	}
	return NewMapper(dev.Info(), dev.Fields(), SelectQuirks(quirks, dev.Info().VendorID))
}

func TestCyberPowerMapping(t *testing.T) {
	dev, _ := newCyberPower(t, nil)
	m := mapperFor(t, dev)
	if got := strings.Join(m.Quirks(), ","); got != "generic,cyberpower" {
		t.Errorf("quirks = %s", got)
	}
	values, err := dev.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	vars := m.Vars(values, func(i int) (string, error) {
		if i == 4 {
			return "PbAc", nil
		}
		return "", errors.New("none")
	})
	// Values as the real CP1500PFCLCD reported them on line power.
	want := map[string]string{
		"ups.status":              "OL",
		"battery.charge":          "100",
		"battery.charge.low":      "10",
		"battery.charge.warning":  "20",
		"battery.runtime":         "1150",
		"battery.runtime.low":     "300",
		"battery.voltage":         "27.4",
		"battery.voltage.nominal": "24.0",
		"battery.type":            "PbAc",
		"input.voltage":           "123.0",
		"input.voltage.nominal":   "120",
		"input.transfer.low":      "100",
		"output.voltage":          "123.0",
		"ups.load":                "37",
		"ups.realpower":           "378",
		"ups.power":               "379",
		"ups.realpower.nominal":   "1000",
		"ups.beeper.status":       "enabled",
		"ups.test.result":         "No test initiated",
		"ups.timer.shutdown":      "-1",
		"device.mfr":              "CPS",
		"device.model":            "CP1500PFCLCDa",
		"ups.vendorid":            "0764",
		"ups.productid":           "0601",
		"driver.name":             "usbhid",
	}
	for k, w := range want {
		if vars[k] != w {
			t.Errorf("%s = %q, want %q", k, vars[k], w)
		}
	}

	cmds := map[string]bool{}
	for _, c := range m.Commands() {
		cmds[c.Name] = true
	}
	for _, c := range []string{"beeper.enable", "beeper.disable", "beeper.mute", "test.battery.start.quick", "test.battery.start.deep", "test.battery.stop"} {
		if !cmds[c] {
			t.Errorf("missing command %s (have %v)", c, cmds)
		}
	}
	if got := strings.Join(m.Writable(), ","); got != "battery.charge.low,battery.runtime.low,input.transfer.low" {
		t.Errorf("writable = %s", got)
	}
}

func TestStatusFlags(t *testing.T) {
	tests := []struct {
		change map[string]int64
		want   string
	}{
		{map[string]int64{"UPS.PowerSummary.PresentStatus.ACPresent": 0, "UPS.PowerSummary.PresentStatus.Discharging": 1}, "OB DISCHRG"},
		{map[string]int64{"UPS.PowerSummary.PresentStatus.ACPresent": 0, "UPS.PowerSummary.PresentStatus.Discharging": 1,
			"UPS.PowerSummary.PresentStatus.BelowRemainingCapacityLimit": 1, "UPS.PowerSummary.PresentStatus.RemainingTimeLimitExpired": 1}, "OB DISCHRG LB"},
		{map[string]int64{"UPS.PowerSummary.PresentStatus.Charging": 1, "UPS.Output.Boost": 1}, "OL CHRG BOOST"},
		{map[string]int64{"UPS.Output.Overload": 1}, "OL OVER"},
	}
	for _, tt := range tests {
		dev, _ := newCyberPower(t, tt.change)
		values, _ := dev.Read(context.Background())
		if got := mapperFor(t, dev).Vars(values, nil)["ups.status"]; got != tt.want {
			t.Errorf("%v: status %q, want %q", tt.change, got, tt.want)
		}
	}
}

func TestCommandsAndSetVar(t *testing.T) {
	dev, _ := newCyberPower(t, nil)
	d, err := newDevice(dev, time10ms, "", quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.RunCommand(ctx, "beeper.disable", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.SetVar(ctx, "battery.charge.low", "25"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetVar(ctx, "input.transfer.low", "90"); !errors.Is(err, sdk.ErrInvalid) {
		t.Errorf("out-of-range transfer voltage: %v", err)
	}
	if err := d.SetVar(ctx, "ups.load", "5"); !errors.Is(err, sdk.ErrNotSupported) {
		t.Errorf("read-only variable: %v", err)
	}
	if err := d.RunCommand(ctx, "load.off", ""); !errors.Is(err, sdk.ErrNotSupported) {
		t.Errorf("unmapped command: %v", err)
	}
	values, _ := dev.Read(ctx)
	vars := d.m.Vars(values, nil)
	if vars["ups.beeper.status"] != "disabled" || vars["battery.charge.low"] != "25" {
		t.Errorf("after writes: beeper %s, charge.low %s", vars["ups.beeper.status"], vars["battery.charge.low"])
	}
	// Writes must not disturb other fields in the same report.
	if vars["battery.charge.warning"] != "20" {
		t.Errorf("battery.charge.warning changed to %s", vars["battery.charge.warning"])
	}
}

const time10ms = 10_000_000

func fakeDriver(t *testing.T, infos ...DeviceInfo) (*Driver, *fakeBackend) {
	t.Helper()
	dev, be := newCyberPower(t, nil)
	return &Driver{
		enumerate: func() ([]DeviceInfo, error) { return infos, nil },
		open:      func(DeviceInfo) (Device, error) { return dev, nil },
	}, be
}

func TestDeviceSelection(t *testing.T) {
	keyboard := DeviceInfo{VendorID: 0x046d, ProductID: 0xc31c, Product: "Keyboard", Usage: hid.NewUsage(1, 6)}
	second := cyberPowerInfo
	second.Serial, second.Path = "OTHER", "/dev/hidraw8"

	d, _ := fakeDriver(t, keyboard)
	if _, err := d.find(Options{}); err == nil || !strings.Contains(err.Error(), "no USB UPS found") {
		t.Errorf("no UPS: %v", err)
	}
	d, _ = fakeDriver(t, keyboard, cyberPowerInfo)
	if got, err := d.find(Options{}); err != nil || got.Serial != cyberPowerInfo.Serial {
		t.Errorf("single UPS: %v %v", got, err)
	}
	d, _ = fakeDriver(t, cyberPowerInfo, second)
	if _, err := d.find(Options{}); err == nil || !strings.Contains(err.Error(), "add `serial:`") {
		t.Errorf("two UPSes: %v", err)
	}
	if got, err := d.find(Options{Serial: "OTHER"}); err != nil || got.Path != "/dev/hidraw8" {
		t.Errorf("by serial: %v %v", got, err)
	}
	if _, err := d.find(Options{ID: "051d"}); err == nil || !strings.Contains(err.Error(), "found:") {
		t.Errorf("no match: %v", err)
	}
	if got, err := d.find(Options{ID: "0764:0601", Vendor: "cps", Path: "/DEV/HIDRAW7"}); err != nil || got.Serial != cyberPowerInfo.Serial {
		t.Errorf("by id/vendor/path: %v %v", got, err)
	}

	found, _ := d.Discover(context.Background())
	if len(found) != 2 || found[0].Config["serial"] != cyberPowerInfo.Serial {
		t.Errorf("Discover = %+v", found)
	}
}

func TestOptions(t *testing.T) {
	if err := New().ValidateConfig(sdk.Config{"id": "0764:0601", "interval": "5s"}); err != nil {
		t.Error(err)
	}
	for _, cfg := range []sdk.Config{{"id": "764"}, {"interval": "10ms"}, {"serail": "x"}} {
		if err := New().ValidateConfig(cfg); err == nil {
			t.Errorf("%v: no error", cfg)
		}
	}
}

func TestWatchReconnectsOnFailure(t *testing.T) {
	d, be := fakeDriver(t, cyberPowerInfo)
	dev, err := d.Open(context.Background(), sdk.Config{"interval": "1s"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan sdk.Update, 4)
	errc := make(chan error, 1)
	go func() { errc <- dev.Watch(context.Background(), func(u sdk.Update) { got <- u }) }()
	if u := <-got; u.Vars["ups.status"] != "OL" {
		t.Fatalf("first update %v", u.Vars["ups.status"])
	}
	be.mu.Lock()
	be.failAll = true
	be.mu.Unlock()
	dev.(*device).report()
	if err := <-errc; err == nil || !strings.Contains(err.Error(), "unplugged") {
		t.Errorf("Watch returned %v, want the read error", err)
	}
}

func TestCaptureAndReplay(t *testing.T) {
	d, _ := fakeDriver(t, cyberPowerInfo)
	n := 0
	c, err := d.CaptureDevice(context.Background(), CaptureOptions{Samples: 2, Interval: 1,
		Progress: func(int, map[string]string) { n++ }})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(c.Samples) != 2 || c.ReportDescriptor == "" {
		t.Fatalf("capture: progress %d, %d samples, descriptor %d chars", n, len(c.Samples), len(c.ReportDescriptor))
	}
	if c.Device.Serial != redacted {
		t.Errorf("serial not redacted: %s", c.Device.Serial)
	}

	path := filepath.Join(t.TempDir(), "capture.json")
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewReplay()
	if err := r.ValidateConfig(sdk.Config{"file": path}); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateConfig(sdk.Config{}); err == nil {
		t.Error("missing file accepted")
	}
	dev, err := r.Open(context.Background(), sdk.Config{"file": path, "interval": "100ms"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan sdk.Update, 8)
	go func() { _ = dev.Watch(ctx, func(u sdk.Update) { got <- u }) }()
	u := <-got
	if u.Vars["battery.runtime"] != "1150" || u.Vars["ups.status"] != "OL" || u.Vars["device.serial"] != redacted {
		t.Errorf("replayed vars: %v", u.Vars)
	}
	if err := dev.RunCommand(ctx, "beeper.disable", ""); err != nil {
		t.Fatal(err)
	}
	for u = range got {
		if u.Vars["ups.beeper.status"] == "disabled" {
			break
		}
	}
}

func TestQuirkFileErrors(t *testing.T) {
	bad := `
name: x
match: {vendor_ids: [764]}
vars:
  battery.charge: {paths: [], convert: fahrenheit}
flags:
  - {flag: XX, paths: [a]}
fixups: [nope]
`
	_, err := ParseQuirkFile("x.yaml", []byte(bad))
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"4 hex digits", "no paths", "fahrenheit", `"XX"`, `"nope"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if _, err := ParseQuirkFile("y.yaml", []byte("name: y\nvarz: {}\n")); err == nil {
		t.Error("unknown key accepted")
	}
}

func TestUserQuirksOverride(t *testing.T) {
	dir := t.TempDir()
	override := "name: mine\nmatch: {vendor_ids: [\"0764\"]}\nvars:\n  ups.load: {paths: [UPS.Output.PercentLoad], scale: 2, decimals: 0}\n"
	if err := os.WriteFile(filepath.Join(dir, "mine.yaml"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	quirks, err := LoadQuirks(dir)
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := newCyberPower(t, nil)
	m := NewMapper(dev.Info(), dev.Fields(), SelectQuirks(quirks, 0x0764))
	values, _ := dev.Read(context.Background())
	if got := m.Vars(values, nil)["ups.load"]; got != "74" {
		t.Errorf("ups.load = %s, want 74 from the override", got)
	}
}

// TestRealCapture replays a capture of a real CyberPower CP1500PFCLCD,
// recorded on Windows, through the full mapping.
func TestRealCapture(t *testing.T) {
	c, err := ReadCapture("testdata/cyberpower-cp1500pfclcd-windows.json")
	if err != nil {
		t.Fatal(err)
	}
	dev := newReplayDevice(c, false)
	values, _ := dev.Read(context.Background())
	vars := mapperFor(t, dev).Vars(values, dev.String)
	want := map[string]string{
		"ups.status":              "OL",
		"battery.charge":          "100",
		"battery.type":            "PbAcid",
		"battery.voltage.nominal": "24.0",
		"input.voltage.nominal":   "120",
		"ups.realpower.nominal":   "1000",
		"ups.power.nominal":       "1500",
		"ups.beeper.status":       "enabled",
		"ups.test.result":         "No test initiated",
		"ups.timer.shutdown":      "-1", // the descriptor scales -1 to -60
		"device.model":            "CP1500PFCLCDa",
	}
	for k, w := range want {
		if vars[k] != w {
			t.Errorf("%s = %q, want %q", k, vars[k], w)
		}
	}
	for _, k := range []string{"battery.runtime", "battery.voltage", "input.voltage", "output.voltage", "ups.load", "ups.realpower"} {
		if vars[k] == "" {
			t.Errorf("%s missing", k)
		}
	}
}
