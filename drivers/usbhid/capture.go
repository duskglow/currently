package usbhid

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/duskglow/currently/drivers/usbhid/hid"
	"github.com/duskglow/currently/pkg/sdk"
)

// CaptureFormat identifies capture files.
const CaptureFormat = "currently-hid-capture/1"

// Capture is a recording of a UPS: its fields and a series of raw reads.
// Captures make bug reports and new quirk files possible without the
// hardware, and the replay driver plays them back.
type Capture struct {
	Format           string     `json:"format"`
	Captured         time.Time  `json:"captured"`
	CurrentlyVersion string     `json:"currently_version"`
	OS               string     `json:"os"`
	Device           DeviceInfo `json:"device"`
	// ReportDescriptor is the raw descriptor in hex, when the platform
	// exposes it (not on Windows).
	ReportDescriptor string         `json:"report_descriptor,omitempty"`
	Strings          map[int]string `json:"strings,omitempty"`
	Fields           []hid.Field    `json:"fields"`
	Samples          []Sample       `json:"samples"`
	// Unmapped lists fields no quirk rule uses (informational).
	Unmapped []string `json:"unmapped,omitempty"`
	// ReportErrors lists the reports that could not be read in the first
	// sample, with their errors.
	ReportErrors map[string]string `json:"report_errors,omitempty"`
	// RawReports holds the bytes of each report in the first sample, in
	// hex, when the platform reads whole reports (not on Windows).
	RawReports map[string]string `json:"raw_reports,omitempty"`
}

// Sample is one read of every report.
type Sample struct {
	// OffsetMS is the time since the first sample.
	OffsetMS int64            `json:"t_ms"`
	Values   map[string]int64 `json:"values"`
}

// CaptureOptions control a capture.
type CaptureOptions struct {
	Device   Options
	Samples  int
	Interval time.Duration
	// KeepSerial keeps the serial number; by default it is replaced so
	// captures can be shared.
	KeepSerial       bool
	CurrentlyVersion string
	// Progress, if set, is called after each sample with the mapped
	// variables.
	Progress func(n int, vars map[string]string)
}

const redacted = "REDACTED"

// CaptureDevice records a UPS.
func (d *Driver) CaptureDevice(ctx context.Context, o CaptureOptions) (*Capture, error) {
	info, err := d.find(o.Device)
	if err != nil {
		return nil, err
	}
	dev, err := d.open(info)
	if err != nil {
		return nil, err
	}
	defer dev.Close()

	quirks, err := LoadQuirks(o.Device.QuirksDir)
	if err != nil {
		return nil, err
	}
	m := NewMapper(info, dev.Fields(), SelectQuirks(quirks, info.VendorID))

	c := &Capture{
		Format: CaptureFormat, Captured: time.Now().UTC(), CurrentlyVersion: o.CurrentlyVersion,
		OS: runtime.GOOS + "/" + runtime.GOARCH, Device: info, Fields: dev.Fields(), Strings: map[int]string{},
		Unmapped: m.Unmapped(dev.Fields()),
	}
	if rd, ok := dev.(interface{ Descriptor() []byte }); ok {
		c.ReportDescriptor = hex.EncodeToString(rd.Descriptor())
	}

	start := time.Now()
	for n := 0; n < max(o.Samples, 1); n++ {
		if n > 0 {
			select {
			case <-ctx.Done():
				return c, ctx.Err()
			case <-time.After(o.Interval):
			}
		}
		values, err := dev.Read(ctx)
		if err != nil {
			return c, err
		}
		c.Samples = append(c.Samples, Sample{OffsetMS: time.Since(start).Milliseconds(), Values: values})
		if re, ok := dev.(interface{ ReportErrors() map[string]string }); ok && n == 0 {
			if errs := re.ReportErrors(); len(errs) > 0 {
				c.ReportErrors = errs
			}
		}
		// Reports hold the serial number's string index, not the string, so
		// they can be shared.
		if rr, ok := dev.(interface{ RawReports() map[string]string }); ok && n == 0 {
			c.RawReports = rr.RawReports()
		}
		if n == 0 {
			// String indexes are the values of fields named i<Something>.
			for _, f := range c.Fields {
				name := f.Usage.Name()
				if strings.HasPrefix(name, "i") && len(name) > 1 && values[f.Path] > 0 {
					idx := int(values[f.Path])
					if s, err := dev.String(idx); err == nil {
						c.Strings[idx] = s
					}
				}
			}
		}
		if o.Progress != nil {
			o.Progress(n+1, m.Vars(values, dev.String))
		}
	}
	if !o.KeepSerial && c.Device.Serial != "" {
		serial := c.Device.Serial
		c.Device.Serial = redacted
		c.Device.Path = strings.ReplaceAll(c.Device.Path, serial, redacted)
		for k, s := range c.Strings {
			c.Strings[k] = strings.ReplaceAll(s, serial, redacted)
		}
	}
	return c, nil
}

// ReadCapture loads a capture file.
func ReadCapture(path string) (*Capture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Capture
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Format != CaptureFormat {
		return nil, fmt.Errorf("%s: not a Currently HID capture (format %q, want %q)", path, c.Format, CaptureFormat)
	}
	if len(c.Samples) == 0 {
		return nil, fmt.Errorf("%s: capture has no samples", path)
	}
	return &c, nil
}

// ReplayDriverName is the name of the replay driver.
const ReplayDriverName = "replay"

// ReplayDriver plays back a capture as if the UPS were attached, for
// tests, demos and developing quirk files.
type ReplayDriver struct{}

// NewReplay returns the replay driver.
func NewReplay() *ReplayDriver { return &ReplayDriver{} }

type replayOptions struct {
	File      string        `yaml:"file"`
	Interval  time.Duration `yaml:"interval"`
	Loop      *bool         `yaml:"loop"`
	QuirksDir string        `yaml:"quirks_dir"`
}

func parseReplayOptions(cfg sdk.Config) (replayOptions, error) {
	o := replayOptions{Interval: 2 * time.Second}
	if err := cfg.Decode(&o); err != nil {
		return o, err
	}
	if o.File == "" {
		return o, errors.New("file is required: the capture to replay (from `currently debug capture`)")
	}
	if o.Interval < 100*time.Millisecond {
		return o, errors.New("interval must be at least 100ms")
	}
	return o, nil
}

// Info implements sdk.Driver.
func (*ReplayDriver) Info() sdk.Info {
	return sdk.Info{Name: ReplayDriverName, Kind: sdk.KindDriver, Version: Version,
		Description: "Plays back a USB UPS capture made with `currently debug capture`"}
}

// Discover implements sdk.Driver.
func (*ReplayDriver) Discover(context.Context) ([]sdk.Discovered, error) { return nil, nil }

// ValidateConfig implements sdk.Driver.
func (*ReplayDriver) ValidateConfig(cfg sdk.Config) error {
	o, err := parseReplayOptions(cfg)
	if err != nil {
		return err
	}
	_, err = ReadCapture(o.File)
	return err
}

// Open implements sdk.Driver.
func (*ReplayDriver) Open(_ context.Context, cfg sdk.Config, log *slog.Logger) (sdk.Device, error) {
	o, err := parseReplayOptions(cfg)
	if err != nil {
		return nil, err
	}
	c, err := ReadCapture(o.File)
	if err != nil {
		return nil, err
	}
	loop := o.Loop == nil || *o.Loop
	return newDevice(newReplayDevice(c, loop), o.Interval, o.QuirksDir, log)
}

// replayDevice implements Device from a capture.
type replayDevice struct {
	mu        sync.Mutex
	c         *Capture
	loop      bool
	next      int
	overrides map[string]int64 // fields written by commands
}

func newReplayDevice(c *Capture, loop bool) *replayDevice {
	return &replayDevice{c: c, loop: loop, overrides: map[string]int64{}}
}

func (r *replayDevice) Info() DeviceInfo    { return r.c.Device }
func (r *replayDevice) Fields() []hid.Field { return r.c.Fields }

func (r *replayDevice) Read(context.Context) (map[string]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.c.Samples[r.next]
	switch {
	case r.next+1 < len(r.c.Samples):
		r.next++
	case r.loop:
		r.next = 0
	}
	values := maps.Clone(s.Values)
	maps.Copy(values, r.overrides)
	return values, nil
}

func (r *replayDevice) Write(path string, value int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overrides[path] = value
	return nil
}

func (r *replayDevice) String(index int) (string, error) {
	if s, ok := r.c.Strings[index]; ok {
		return s, nil
	}
	return "", fmt.Errorf("string %d not in capture", index)
}

func (r *replayDevice) Close() error { return nil }
