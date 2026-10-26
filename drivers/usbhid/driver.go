// Package usbhid is the driver for UPSes that follow the USB HID Power
// Device class, which covers most consumer and small-business UPSes (APC,
// CyberPower, Eaton, Tripp Lite, ...). It reads the device through the
// operating system's HID driver, so no extra drivers or libraries are
// needed.
package usbhid

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nut-ng/nut-ng/internal/paths"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

const (
	// DriverName is the name used in `driver:` in nutng.yaml.
	DriverName = "usbhid"
	// Version of the usbhid driver.
	Version = "0.1.0"
)

// Options select a device and tune polling.
type Options struct {
	// ID is "vvvv" or "vvvv:pppp" in hex, as shown by `nutng detect`.
	ID string `yaml:"id"`
	// Serial matches the USB serial number exactly.
	Serial string `yaml:"serial"`
	// Vendor and Product match the USB manufacturer and product strings
	// (case-insensitive substring).
	Vendor  string `yaml:"vendor"`
	Product string `yaml:"product"`
	// Path is the OS device path; it can change when the UPS is replugged,
	// so prefer serial.
	Path string `yaml:"path"`
	// Interval between reads.
	Interval time.Duration `yaml:"interval"`
	// QuirksDir holds extra quirk files (default <config dir>/quirks.d).
	QuirksDir string `yaml:"quirks_dir"`
}

var idRE = regexp.MustCompile(`^([0-9a-fA-F]{4})(?::([0-9a-fA-F]{4}))?$`)

func parseOptions(cfg sdk.Config) (Options, error) {
	o := Options{Interval: 2 * time.Second, QuirksDir: filepath.Join(paths.ConfigDir(), "quirks.d")}
	if err := cfg.Decode(&o); err != nil {
		return o, err
	}
	var errs []error
	if o.ID != "" && !idRE.MatchString(o.ID) {
		errs = append(errs, fmt.Errorf(`id %q must be "vvvv" or "vvvv:pppp" in hex, quoted (for example "0764:0601")`, o.ID))
	}
	if o.Interval < 500*time.Millisecond {
		errs = append(errs, errors.New("interval must be at least 500ms"))
	}
	return o, errors.Join(errs...)
}

// Selective reports whether the options pick a specific device.
func (o Options) Selective() bool {
	return o.ID != "" || o.Serial != "" || o.Vendor != "" || o.Product != "" || o.Path != ""
}

func (o Options) matches(d DeviceInfo) bool {
	if m := idRE.FindStringSubmatch(o.ID); m != nil {
		vid, _ := strconv.ParseUint(m[1], 16, 16)
		if uint16(vid) != d.VendorID {
			return false
		}
		if m[2] != "" {
			pid, _ := strconv.ParseUint(m[2], 16, 16)
			if uint16(pid) != d.ProductID {
				return false
			}
		}
	}
	contains := func(s, sub string) bool { return strings.Contains(strings.ToLower(s), strings.ToLower(sub)) }
	return (o.Serial == "" || o.Serial == d.Serial) &&
		(o.Vendor == "" || contains(d.Manufacturer, o.Vendor)) &&
		(o.Product == "" || contains(d.Product, o.Product)) &&
		(o.Path == "" || strings.EqualFold(o.Path, d.Path))
}

// Driver is the usbhid driver.
type Driver struct {
	enumerate func() ([]DeviceInfo, error)
	open      func(DeviceInfo) (Device, error)
}

// New returns the usbhid driver.
func New() *Driver { return &Driver{enumerate: enumerate, open: openDevice} }

// Info implements sdk.Driver.
func (*Driver) Info() sdk.Info {
	return sdk.Info{Name: DriverName, Kind: sdk.KindDriver, Version: Version,
		Description: "USB UPSes that follow the HID Power Device class (APC, CyberPower, Eaton, ...)"}
}

// ValidateConfig implements sdk.Driver.
func (*Driver) ValidateConfig(cfg sdk.Config) error {
	o, err := parseOptions(cfg)
	if err != nil {
		return err
	}
	_, err = LoadQuirks(o.QuirksDir)
	return err
}

// UPSes lists attached UPSes.
func (d *Driver) UPSes() ([]DeviceInfo, error) {
	all, err := d.enumerate()
	if err != nil {
		return nil, err
	}
	var out []DeviceInfo
	for _, info := range all {
		if info.IsPowerDevice() {
			out = append(out, info)
		}
	}
	return out, nil
}

// Discover implements sdk.Driver.
func (d *Driver) Discover(context.Context) ([]sdk.Discovered, error) {
	upses, err := d.UPSes()
	if err != nil {
		return nil, err
	}
	var out []sdk.Discovered
	for _, u := range upses {
		cfg := sdk.Config{"id": u.ID()}
		if u.Serial != "" {
			cfg["serial"] = u.Serial
		}
		out = append(out, sdk.Discovered{Driver: DriverName, ID: u.ID(), Name: u.Name(), Description: u.Description(), Config: cfg})
	}
	return out, nil
}

// NoDeviceHelp explains how to troubleshoot a UPS that is not found.
func NoDeviceHelp() string {
	switch runtime.GOOS {
	case "windows":
		return "Check that the UPS's USB cable is plugged into this computer; it should appear in Device Manager under Batteries as \"HID UPS Battery\"."
	case "linux":
		return "Check that the UPS's USB cable is plugged in and that it appears in `lsusb` and under /sys/class/hidraw."
	default:
		return "USB UPS access on " + runtime.GOOS + " is not supported yet."
	}
}

func (d *Driver) find(o Options) (DeviceInfo, error) {
	upses, err := d.UPSes()
	if err != nil {
		return DeviceInfo{}, err
	}
	var matched []DeviceInfo
	for _, u := range upses {
		if o.matches(u) {
			matched = append(matched, u)
		}
	}
	list := func(ds []DeviceInfo) string {
		parts := make([]string, len(ds))
		for i, u := range ds {
			parts[i] = u.Description()
		}
		return strings.Join(parts, "; ")
	}
	switch {
	case len(upses) == 0:
		return DeviceInfo{}, errors.New("no USB UPS found. " + NoDeviceHelp())
	case len(matched) == 0:
		return DeviceInfo{}, fmt.Errorf("no USB UPS matches the configured id/serial/vendor/product/path; found: %s", list(upses))
	case len(matched) > 1 && !o.Selective():
		return DeviceInfo{}, fmt.Errorf("found %d USB UPSes; add `serial:` to the UPS block to choose one: %s", len(matched), list(matched))
	}
	return matched[0], nil
}

// Open implements sdk.Driver.
func (d *Driver) Open(_ context.Context, cfg sdk.Config, log *slog.Logger) (sdk.Device, error) {
	o, err := parseOptions(cfg)
	if err != nil {
		return nil, err
	}
	info, err := d.find(o)
	if err != nil {
		return nil, err
	}
	dev, err := d.open(info)
	if err != nil {
		return nil, err
	}
	return newDevice(dev, o.Interval, o.QuirksDir, log)
}

// device adapts a Device and its Mapper to sdk.Device.
type device struct {
	dev      Device
	m        *Mapper
	interval time.Duration
	log      *slog.Logger
	poke     chan struct{}
	once     sync.Once
}

func newDevice(dev Device, interval time.Duration, quirksDir string, log *slog.Logger) (*device, error) {
	quirks, err := LoadQuirks(quirksDir)
	if err != nil {
		dev.Close()
		return nil, err
	}
	info := dev.Info()
	m := NewMapper(info, dev.Fields(), SelectQuirks(quirks, info.VendorID))
	log.Info("USB UPS opened", "device", info.Description(), "fields", len(dev.Fields()), "quirks", strings.Join(m.Quirks(), ","))
	return &device{dev: dev, m: m, interval: interval, log: log, poke: make(chan struct{}, 1)}, nil
}

func (d *device) Watch(ctx context.Context, sink func(sdk.Update)) error {
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		values, err := d.dev.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		sink(sdk.Update{Vars: d.m.Vars(values, d.dev.String)})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		case <-d.poke:
		}
	}
}

func (d *device) Commands() []sdk.Command { return d.m.Commands() }
func (d *device) Writable() []string      { return d.m.Writable() }

func (d *device) RunCommand(_ context.Context, name, _ string) error {
	path, raw, err := d.m.Command(name)
	if err != nil {
		return err
	}
	if err := d.dev.Write(path, raw); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	d.report()
	return nil
}

func (d *device) SetVar(_ context.Context, name, value string) error {
	path, raw, err := d.m.SetVar(name, value)
	if err != nil {
		return err
	}
	if err := d.dev.Write(path, raw); err != nil {
		return fmt.Errorf("set %s: %w", name, err)
	}
	d.report()
	return nil
}

func (d *device) report() {
	select {
	case d.poke <- struct{}{}:
	default:
	}
}

func (d *device) Close() error {
	var err error
	d.once.Do(func() { err = d.dev.Close() })
	return err
}
