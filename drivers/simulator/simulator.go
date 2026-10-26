// Package simulator is a driver for a simulated UPS. It needs no hardware,
// so it is useful for trying Currently out, for tests, and for rehearsing
// power failures (`currently cmd <ups> sim.power.fail`).
package simulator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/duskglow/currently/pkg/sdk"
)

const (
	// DriverName is the name used in `driver:` in currently.yaml.
	DriverName = "simulator"
	// Version of the simulator driver.
	Version = "1.0.0"
)

// Scenarios for mains power.
const (
	ScenarioOnline    = "online"    // mains is always up
	ScenarioOnBattery = "onbattery" // mains is always down
	ScenarioCycle     = "cycle"     // online_for up, then outage_for down, repeating
)

// Options are the simulator's settings in a UPS block. Durations are in
// simulated time; speed makes simulated time run faster than real time.
type Options struct {
	Manufacturer     string        `yaml:"manufacturer"`
	Model            string        `yaml:"model"`
	Serial           string        `yaml:"serial"`
	Scenario         string        `yaml:"scenario"`
	Interval         time.Duration `yaml:"interval"`
	Speed            float64       `yaml:"speed"`
	Runtime          time.Duration `yaml:"runtime"`
	RechargeTime     time.Duration `yaml:"recharge_time"`
	Load             float64       `yaml:"load"`
	Charge           float64       `yaml:"charge"`
	NominalVoltage   float64       `yaml:"nominal_voltage"`
	NominalFrequency float64       `yaml:"nominal_frequency"`
	PowerNominal     float64       `yaml:"power_nominal"`
	OnlineFor        time.Duration `yaml:"online_for"`
	OutageFor        time.Duration `yaml:"outage_for"`
}

// DefaultOptions returns the settings used for keys left out of the config.
func DefaultOptions() Options {
	return Options{
		Manufacturer:     "Currently",
		Model:            "Simulator 1500",
		Serial:           "SIM0001",
		Scenario:         ScenarioOnline,
		Interval:         2 * time.Second,
		Speed:            1,
		Runtime:          30 * time.Minute,
		RechargeTime:     time.Hour,
		Load:             35,
		Charge:           100,
		NominalVoltage:   230,
		NominalFrequency: 50,
		PowerNominal:     1500,
		OnlineFor:        2 * time.Minute,
		OutageFor:        time.Minute,
	}
}

func parseOptions(cfg sdk.Config) (Options, error) {
	o := DefaultOptions()
	if err := cfg.Decode(&o); err != nil {
		return o, err
	}
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	switch o.Scenario {
	case ScenarioOnline, ScenarioOnBattery, ScenarioCycle:
	default:
		errs = append(errs, fmt.Errorf("scenario %q is not one of %s, %s, %s",
			o.Scenario, ScenarioOnline, ScenarioOnBattery, ScenarioCycle))
	}
	check(o.Interval >= 100*time.Millisecond, "interval must be at least 100ms")
	check(o.Speed > 0, "speed must be greater than 0")
	check(o.Runtime > 0, "runtime must be greater than 0")
	check(o.RechargeTime > 0, "recharge_time must be greater than 0")
	check(o.Load >= 0 && o.Load <= 100, "load must be between 0 and 100")
	check(o.Charge >= 0 && o.Charge <= 100, "charge must be between 0 and 100")
	check(o.NominalVoltage > 0, "nominal_voltage must be greater than 0")
	check(o.NominalFrequency > 0, "nominal_frequency must be greater than 0")
	check(o.PowerNominal > 0, "power_nominal must be greater than 0")
	if o.Scenario == ScenarioCycle {
		check(o.OnlineFor > 0 && o.OutageFor > 0, "online_for and outage_for must be greater than 0")
	}
	return o, errors.Join(errs...)
}

// Driver is the simulator driver.
type Driver struct{}

// New returns the simulator driver.
func New() *Driver { return &Driver{} }

// Info implements sdk.Driver.
func (*Driver) Info() sdk.Info {
	return sdk.Info{Name: DriverName, Kind: sdk.KindDriver, Version: Version,
		Description: "Simulated UPS for demos, tests and power-failure rehearsals"}
}

// Discover implements sdk.Driver. Simulated UPSes are only created from config.
func (*Driver) Discover(context.Context) ([]sdk.Discovered, error) { return nil, nil }

// ValidateConfig implements sdk.Driver.
func (*Driver) ValidateConfig(cfg sdk.Config) error {
	_, err := parseOptions(cfg)
	return err
}

// Open implements sdk.Driver.
func (*Driver) Open(_ context.Context, cfg sdk.Config, log *slog.Logger) (sdk.Device, error) {
	o, err := parseOptions(cfg)
	if err != nil {
		return nil, err
	}
	return &device{m: newModel(o), log: log, poke: make(chan struct{}, 1)}, nil
}

// Commands supported by every simulated UPS.
var commands = []sdk.Command{
	sdk.NewCommand("beeper.enable"),
	sdk.NewCommand("beeper.disable"),
	sdk.NewCommand("test.battery.start"),
	sdk.NewCommand("test.battery.start.quick"),
	sdk.NewCommand("test.battery.stop"),
	{Name: "load.off.delay", Description: "Turn off the load after a delay (seconds, default 20)"},
	{Name: "shutdown.return", Description: "Turn off the load after a delay (seconds, default 20), and back on when mains power returns"},
	{Name: "shutdown.stop", Description: "Cancel a pending load.off.delay or shutdown.return"},
	{Name: "sim.power.fail", Description: "Simulate a mains power failure"},
	{Name: "sim.power.restore", Description: "End a simulated power failure"},
	{Name: "sim.power.auto", Description: "Return mains power to the configured scenario"},
}

var writable = []string{"battery.charge.low", "battery.runtime.low", "ups.load"}

type device struct {
	mu   sync.Mutex
	m    *model
	log  *slog.Logger
	poke chan struct{} // asks Watch to report right away
}

func (d *device) Watch(ctx context.Context, sink func(sdk.Update)) error {
	d.mu.Lock()
	interval := d.m.opts.Interval
	simStep := time.Duration(float64(interval) * d.m.opts.Speed)
	d.m.step(0)
	vars := d.m.vars()
	d.mu.Unlock()
	sink(sdk.Update{Vars: vars})

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			d.mu.Lock()
			d.m.step(simStep)
		case <-d.poke:
			d.mu.Lock()
			d.m.step(0)
		}
		vars := d.m.vars()
		d.mu.Unlock()
		sink(sdk.Update{Vars: vars})
	}
}

func (d *device) Commands() []sdk.Command { return commands }
func (d *device) Writable() []string      { return writable }

func (d *device) RunCommand(_ context.Context, name, param string) error {
	delay := 20 * time.Second
	if p := strings.TrimSpace(param); p != "" {
		secs, err := strconv.ParseFloat(p, 64)
		if err != nil || secs < 0 {
			return fmt.Errorf("%s: delay must be a number of seconds, got %q: %w", name, param, sdk.ErrInvalid)
		}
		delay = time.Duration(secs * float64(time.Second))
	}
	d.mu.Lock()
	m := d.m
	switch name {
	case "load.off.delay":
		m.offAt, m.returnAfter = m.elapsed+max(delay, time.Millisecond), 0
	case "shutdown.return":
		m.offAt, m.returnAfter = m.elapsed+max(delay, time.Millisecond), 30*time.Second
	case "shutdown.stop":
		m.offAt = 0
	case "beeper.enable":
		m.beeper = true
	case "beeper.disable":
		m.beeper = false
	case "test.battery.start", "test.battery.start.quick":
		if m.onBattery {
			d.mu.Unlock()
			return fmt.Errorf("cannot start a battery test while on battery: %w", sdk.ErrInvalid)
		}
		m.testUntil = m.elapsed + testLength
	case "test.battery.stop":
		if m.testUntil > 0 {
			m.testUntil = 0
			m.testResult = "Aborted"
		}
	case "sim.power.fail":
		m.manual = boolPtr(false)
	case "sim.power.restore":
		m.manual = boolPtr(true)
	case "sim.power.auto":
		m.manual = nil
	default:
		d.mu.Unlock()
		return fmt.Errorf("command %q: %w", name, sdk.ErrNotSupported)
	}
	d.mu.Unlock()
	d.report()
	return nil
}

func (d *device) SetVar(_ context.Context, name, value string) error {
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return fmt.Errorf("%s must be a number, got %q: %w", name, value, sdk.ErrInvalid)
	}
	d.mu.Lock()
	switch name {
	case "battery.charge.low", "ups.load":
		if v < 0 || v > 100 {
			d.mu.Unlock()
			return fmt.Errorf("%s must be between 0 and 100: %w", name, sdk.ErrInvalid)
		}
		if name == "ups.load" {
			d.m.load = v
		} else {
			d.m.lowCharge = v
		}
	case "battery.runtime.low":
		if v < 0 {
			d.mu.Unlock()
			return fmt.Errorf("%s must not be negative: %w", name, sdk.ErrInvalid)
		}
		d.m.lowRuntime = v
	default:
		d.mu.Unlock()
		return fmt.Errorf("variable %q: %w", name, sdk.ErrNotSupported)
	}
	d.mu.Unlock()
	d.report()
	return nil
}

func (d *device) Close() error { return nil }

// report asks Watch to send an update now instead of at the next tick.
func (d *device) report() {
	select {
	case d.poke <- struct{}{}:
	default:
	}
}

func boolPtr(b bool) *bool { return &b }
