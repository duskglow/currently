// Command currently-driver-testdriver is a driver plugin for the loader tests.
package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/duskglow/currently/pkg/plugin"
	"github.com/duskglow/currently/pkg/sdk"
)

type driver struct{}

func (driver) Info() sdk.Info {
	return sdk.Info{Name: "testdriver", Version: "1.2.3", Description: "Test driver plugin"}
}

func (driver) Discover(context.Context) ([]sdk.Discovered, error) {
	return []sdk.Discovered{{ID: "t1", Description: "Test UPS", Config: sdk.Config{"serial": "T1"}}}, nil
}

type opts struct {
	Serial     string        `yaml:"serial"`
	CrashAfter time.Duration `yaml:"crash_after"`
}

func (driver) ValidateConfig(cfg sdk.Config) error {
	var o opts
	return cfg.Decode(&o)
}

func (driver) Open(_ context.Context, cfg sdk.Config, log *slog.Logger) (sdk.Device, error) {
	var o opts
	if err := cfg.Decode(&o); err != nil {
		return nil, err
	}
	log.Info("test device opened", "serial", o.Serial)
	return &device{o: o, charge: 100}, nil
}

type device struct {
	o      opts
	charge int
	beeper string
}

func (d *device) Watch(ctx context.Context, sink func(sdk.Update)) error {
	start := time.Now()
	for {
		sink(sdk.Update{Vars: map[string]string{
			"ups.status": "OL", "battery.charge": strconv.Itoa(d.charge), "device.serial": d.o.Serial,
			"ups.beeper.status": d.beeper, "driver.pid": strconv.Itoa(os.Getpid()),
		}})
		if d.o.CrashAfter > 0 && time.Since(start) > d.o.CrashAfter {
			os.Exit(3) // simulate a plugin crash
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (d *device) Commands() []sdk.Command { return []sdk.Command{sdk.NewCommand("beeper.disable")} }
func (d *device) Writable() []string      { return []string{"battery.charge"} }

func (d *device) RunCommand(_ context.Context, name, _ string) error {
	d.beeper = "disabled"
	return nil
}

func (d *device) SetVar(_ context.Context, name, value string) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return sdk.ErrInvalid
	}
	d.charge = n
	return nil
}

func (d *device) Close() error { return nil }

func main() { plugin.ServeDriver(driver{}) }
