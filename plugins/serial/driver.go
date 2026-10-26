package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"

	"github.com/duskglow/currently/pkg/sdk"
)

// Version of the plugin.
const Version = "0.1.0"

// Driver reads UPSes over a serial port.
type Driver struct {
	// open opens a port; tests replace it.
	open func(name string, baud int) (port, error)
}

// Info implements sdk.Driver.
func (Driver) Info() sdk.Info {
	return sdk.Info{Name: "serial", Kind: sdk.KindDriver, Version: Version,
		Description: "Serial UPSes: Megatec/Q1 and APC Smart protocols"}
}

// Options configure one UPS.
type Options struct {
	// Port is the serial device: /dev/ttyS0, /dev/ttyUSB0, COM1, ...
	Port string `yaml:"port"`
	// Protocol is "auto" (default), "megatec" or "apcsmart".
	Protocol string        `yaml:"protocol"`
	Baud     int           `yaml:"baud"`
	Interval time.Duration `yaml:"interval"`
	// Timeout is how long to wait for each reply.
	Timeout time.Duration `yaml:"timeout"`
	// Megatec: the battery voltages that count as empty and full, for the
	// charge estimate, and the number of cells when the UPS reports volts
	// per cell without a rating.
	BatteryVoltageLow  *float64 `yaml:"battery_voltage_low"`
	BatteryVoltageHigh *float64 `yaml:"battery_voltage_high"`
	Cells              int      `yaml:"cells"`
}

func parseOptions(cfg sdk.Config) (Options, error) {
	o := Options{Protocol: "auto", Baud: 2400, Interval: 2 * time.Second, Timeout: time.Second}
	if err := cfg.Decode(&o); err != nil {
		return o, err
	}
	var errs []error
	if strings.TrimSpace(o.Port) == "" {
		errs = append(errs, errors.New("port is required (such as /dev/ttyS0 or COM1)"))
	}
	switch o.Protocol = strings.ToLower(o.Protocol); o.Protocol {
	case "auto", "megatec", "apcsmart":
	case "q1", "blazer":
		o.Protocol = "megatec"
	default:
		errs = append(errs, fmt.Errorf("protocol %q is not auto, megatec or apcsmart", o.Protocol))
	}
	if o.Baud <= 0 {
		errs = append(errs, errors.New("baud must be positive"))
	}
	if o.Interval < 500*time.Millisecond {
		errs = append(errs, errors.New("interval must be at least 500ms"))
	}
	if o.Timeout <= 0 {
		errs = append(errs, errors.New("timeout must be positive"))
	}
	if (o.BatteryVoltageLow == nil) != (o.BatteryVoltageHigh == nil) ||
		o.BatteryVoltageLow != nil && *o.BatteryVoltageLow >= *o.BatteryVoltageHigh {
		errs = append(errs, errors.New("set both battery_voltage_low and battery_voltage_high, low below high"))
	}
	if o.Cells < 0 {
		errs = append(errs, errors.New("cells must not be negative"))
	}
	return o, errors.Join(errs...)
}

// ValidateConfig implements sdk.Driver.
func (Driver) ValidateConfig(cfg sdk.Config) error {
	_, err := parseOptions(cfg)
	return err
}

// Discover implements sdk.Driver. Probing every serial port could upset
// other devices, so serial UPSes are configured by port.
func (Driver) Discover(context.Context) ([]sdk.Discovered, error) { return nil, nil }

func openSerial(name string, baud int) (port, error) {
	p, err := serial.Open(name, &serial.Mode{BaudRate: baud, DataBits: 8, Parity: serial.NoParity,
		StopBits: serial.OneStopBit})
	if err != nil {
		return nil, err
	}
	// Some Megatec cables take power from DTR and need RTS low.
	_ = p.SetDTR(true)
	_ = p.SetRTS(false)
	return p, nil
}

// protocol is one serial UPS protocol.
type protocol interface {
	name() string
	detect(l *link) bool
	static(l *link) map[string]string
	poll(l *link) (map[string]string, error)
	commands() []string
	run(l *link, name, param string, last map[string]string) error
}

// Open implements sdk.Driver.
func (d Driver) Open(_ context.Context, cfg sdk.Config, log *slog.Logger) (sdk.Device, error) {
	o, err := parseOptions(cfg)
	if err != nil {
		return nil, err
	}
	open := d.open
	if open == nil {
		open = openSerial
	}
	p, err := open(o.Port, o.Baud)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", o.Port, err)
	}
	l := newLink(p, o.Timeout)
	var proto protocol
	candidates := []protocol{newAPCSmart(), &megatec{o: o, cells: o.Cells}}
	for _, c := range candidates {
		if o.Protocol != "auto" && c.name() != o.Protocol {
			continue
		}
		if c.detect(l) {
			proto = c
			break
		}
	}
	if proto == nil {
		p.Close()
		what := "an APC Smart or Megatec UPS"
		if o.Protocol != "auto" {
			what = "a " + o.Protocol + " UPS"
		}
		return nil, fmt.Errorf("no answer from %s on %s at %d baud; check the cable (APC needs its smart signalling cable) and the protocol", what, o.Port, o.Baud)
	}
	if proto.name() == "apcsmart" {
		l.pause = 30 * time.Millisecond
	}
	dev := &device{o: o, l: l, proto: proto, log: log, poke: make(chan struct{}, 1)}
	dev.static = proto.static(l)
	log.Info("serial UPS connected", "port", o.Port, "protocol", proto.name(), "model", dev.static["device.model"])
	return dev, nil
}

type device struct {
	o      Options
	l      *link
	proto  protocol
	log    *slog.Logger
	static map[string]string
	poke   chan struct{}

	mu   sync.Mutex
	last map[string]string
}

// Watch implements sdk.Device.
func (d *device) Watch(ctx context.Context, sink func(sdk.Update)) error {
	t := time.NewTicker(d.o.Interval)
	defer t.Stop()
	failures := 0
	for {
		vars, err := d.proto.poll(d.l)
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			failures++
			if failures == 1 || failures%10 == 0 {
				d.log.Warn("serial UPS did not answer", "port", d.o.Port, "error", err, "failures", failures)
			}
		default:
			failures = 0
			all := maps.Clone(d.static)
			maps.Copy(all, vars)
			all["device.type"] = "ups"
			for _, k := range []string{"mfr", "model"} {
				if v := all["device."+k]; v != "" {
					all["ups."+k] = v
				}
			}
			d.mu.Lock()
			d.last = all
			d.mu.Unlock()
			sink(sdk.Update{Vars: all})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		case <-d.poke:
		}
	}
}

// Commands implements sdk.Device.
func (d *device) Commands() []sdk.Command {
	names := d.proto.commands()
	out := make([]sdk.Command, len(names))
	for i, n := range names {
		out[i] = sdk.NewCommand(n)
	}
	return out
}

// Writable implements sdk.Device.
func (d *device) Writable() []string { return nil }

// RunCommand implements sdk.Device.
func (d *device) RunCommand(_ context.Context, name, param string) error {
	d.mu.Lock()
	last := d.last
	d.mu.Unlock()
	if err := d.proto.run(d.l, name, param, last); err != nil {
		return err
	}
	d.log.Info("UPS command sent", "command", name, "param", param)
	select {
	case d.poke <- struct{}{}:
	default:
	}
	return nil
}

// SetVar implements sdk.Device.
func (d *device) SetVar(context.Context, string, string) error { return sdk.ErrNotSupported }

// Close implements sdk.Device.
func (d *device) Close() error { return d.l.p.Close() }
