package simulator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nut-ng/nut-ng/pkg/sdk"
)

func newTestModel(t *testing.T, cfg sdk.Config) *model {
	t.Helper()
	o, err := parseOptions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return newModel(o)
}

func TestModelDrainsAndRecharges(t *testing.T) {
	// 10 minutes of runtime at 50% load; recharge takes 20 minutes.
	m := newTestModel(t, sdk.Config{"scenario": "onbattery", "runtime": "10m", "recharge_time": "20m", "load": 50})
	m.step(0)
	st := m.status()
	if !st.Has(sdk.FlagOnBattery) || !st.Has(sdk.FlagDischarging) {
		t.Fatalf("status = %s", st)
	}
	m.step(5 * time.Minute)
	if m.charge < 49 || m.charge > 51 {
		t.Errorf("after half the runtime, charge = %.1f", m.charge)
	}
	if got := m.vars()["battery.runtime"]; got != "300" {
		t.Errorf("battery.runtime = %s, want 300", got)
	}

	m.step(3 * time.Minute) // 20% left = 120s runtime: low battery
	if !m.status().Has(sdk.FlagLowBattery) {
		t.Errorf("no LB at %.1f%%: %s", m.charge, m.status())
	}

	m.step(5 * time.Minute) // exhausted
	if st := m.status(); !st.Has(sdk.FlagOff) || m.vars()["output.voltage"] != "0.0" {
		t.Errorf("battery empty but status = %s", st)
	}

	m.manual = boolPtr(true)
	m.step(10 * time.Minute)
	st = m.status()
	if !st.Has(sdk.FlagOnline) || !st.Has(sdk.FlagCharging) || st.Has(sdk.FlagOff) {
		t.Errorf("after power returns status = %s", st)
	}
	if m.charge < 49 || m.charge > 51 {
		t.Errorf("after half the recharge time, charge = %.1f", m.charge)
	}
}

func TestModelCycle(t *testing.T) {
	m := newTestModel(t, sdk.Config{"scenario": "cycle", "online_for": "60s", "outage_for": "30s"})
	m.step(30 * time.Second)
	if m.onBattery {
		t.Error("on battery during online phase")
	}
	m.step(40 * time.Second)
	if !m.onBattery {
		t.Error("online during outage phase")
	}
	m.step(30 * time.Second)
	if m.onBattery {
		t.Error("still on battery in next cycle")
	}
}

func TestValidateConfig(t *testing.T) {
	d := New()
	if err := d.ValidateConfig(sdk.Config{"scenario": "cycle", "speed": 10}); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	err := d.ValidateConfig(sdk.Config{"scenario": "storm", "load": 120})
	if err == nil || !strings.Contains(err.Error(), "scenario") || !strings.Contains(err.Error(), "load") {
		t.Errorf("want scenario and load errors, got %v", err)
	}
	if err := d.ValidateConfig(sdk.Config{"scenaro": "cycle"}); err == nil || !strings.Contains(err.Error(), "did you mean") {
		t.Errorf("want typo hint, got %v", err)
	}
}

func TestDeviceCommands(t *testing.T) {
	dev, err := New().Open(context.Background(), sdk.Config{"interval": "1h"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan sdk.Update, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = dev.Watch(ctx, func(u sdk.Update) { updates <- u }) }()

	next := func() sdk.Status {
		t.Helper()
		select {
		case u := <-updates:
			return sdk.ParseStatus(u.Vars["ups.status"])
		case <-time.After(2 * time.Second):
			t.Fatal("no update")
			return nil
		}
	}
	if st := next(); !st.Has(sdk.FlagOnline) {
		t.Fatalf("initial status %s", st)
	}
	if err := dev.RunCommand(ctx, "sim.power.fail", ""); err != nil {
		t.Fatal(err)
	}
	if st := next(); !st.Has(sdk.FlagOnBattery) {
		t.Fatalf("after sim.power.fail status %s", st)
	}
	if err := dev.RunCommand(ctx, "test.battery.start", ""); !errors.Is(err, sdk.ErrInvalid) {
		t.Errorf("battery test on battery: %v", err)
	}
	if err := dev.SetVar(ctx, "ups.load", "150"); !errors.Is(err, sdk.ErrInvalid) {
		t.Errorf("load 150: %v", err)
	}
	if err := dev.SetVar(ctx, "ups.load", "80"); err != nil {
		t.Errorf("load 80: %v", err)
	}
}
