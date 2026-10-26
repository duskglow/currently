package usbhid

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// Power commands are tested only against the fake device; nothing here
// talks to hardware.

func TestPowerCommandsRefuseInexactDelays(t *testing.T) {
	dev, _ := newCyberPower(t, nil)
	m := mapperFor(t, dev)

	// The fake, like the real CP1500PFCLCD, counts delays in minutes, so
	// the default 20 s would round to 0 (cut power now) and is refused.
	for _, cmd := range []string{"load.off.delay", "shutdown.return"} {
		if _, err := m.Command(cmd, ""); !errors.Is(err, sdk.ErrInvalid) || !strings.Contains(err.Error(), "steps of 60") {
			t.Errorf("%s with the 20s default: %v", cmd, err)
		}
		if _, err := m.Command(cmd, "90"); !errors.Is(err, sdk.ErrInvalid) {
			t.Errorf("%s 90: %v", cmd, err)
		}
	}
	if _, err := m.Command("shutdown.return", "soon"); !errors.Is(err, sdk.ErrInvalid) {
		t.Errorf("non-numeric parameter: %v", err)
	}

	writes, err := m.Command("shutdown.return", "120")
	if err != nil {
		t.Fatal(err)
	}
	want := []Write{{"UPS.Output.DelayBeforeStartup", 1}, {"UPS.Output.DelayBeforeShutdown", 2}}
	if len(writes) != 2 || writes[0] != want[0] || writes[1] != want[1] {
		t.Errorf("shutdown.return 120 = %+v, want %+v", writes, want)
	}
	writes, err = m.Command("shutdown.stop", "")
	if err != nil || len(writes) != 1 || writes[0] != (Write{"UPS.Output.DelayBeforeShutdown", -1}) {
		t.Errorf("shutdown.stop = %+v, %v", writes, err)
	}
}

func TestPowerCommandWritesReachTheDevice(t *testing.T) {
	dev, _ := newCyberPower(t, nil)
	d, err := newDevice(dev, time10ms, "", quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.RunCommand(ctx, "shutdown.return", "60"); err != nil {
		t.Fatal(err)
	}
	values, _ := dev.Read(ctx)
	vars := d.m.Vars(values, nil)
	if vars["ups.timer.shutdown"] != "60" || vars["ups.timer.start"] != "60" {
		t.Errorf("timers after shutdown.return 60: shutdown %s, start %s", vars["ups.timer.shutdown"], vars["ups.timer.start"])
	}
	if err := d.RunCommand(ctx, "shutdown.stop", ""); err != nil {
		t.Fatal(err)
	}
	values, _ = dev.Read(ctx)
	if got := d.m.Vars(values, nil)["ups.timer.shutdown"]; got != "-1" {
		t.Errorf("timer after shutdown.stop = %s", got)
	}
}

func TestCommandWriteRuleValidation(t *testing.T) {
	bad := `
name: x
commands:
  a: {paths: [p], writes: [{paths: [q], value: 1}]}
  b: {writes: [{paths: [q]}]}
  c: {writes: [{paths: [q], value: 1, param: true}]}
`
	_, err := ParseQuirkFile("x.yaml", []byte(bad))
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"command a: use either", "command b: write 1 needs exactly one", "command c: write 1 needs exactly one"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}
