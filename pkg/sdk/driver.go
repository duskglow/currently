package sdk

import (
	"context"
	"log/slog"
)

// Driver reads one kind of UPS (for example "usbhid" or "simulator"). The
// core keeps one Driver per kind and calls Open once for every configured
// UPS that uses it.
type Driver interface {
	Info() Info

	// Discover lists devices the driver can find without any configuration.
	// Each result's Config, placed in a UPS block, selects that device.
	// Drivers that cannot discover devices return nil, nil.
	Discover(ctx context.Context) ([]Discovered, error)

	// ValidateConfig checks a UPS block's driver options without touching
	// any hardware. Use Config.Decode for strict decoding.
	ValidateConfig(cfg Config) error

	// Open connects to one device. The core retries with backoff if Open or
	// the device's Watch fails.
	Open(ctx context.Context, cfg Config, log *slog.Logger) (Device, error)
}

// Discovered is a device found by Driver.Discover.
type Discovered struct {
	Driver      string `json:"driver"`
	ID          string `json:"id"`
	Description string `json:"description"`
	Config      Config `json:"config,omitempty"`
}

// Device is one open UPS.
type Device interface {
	// Watch reports values through sink until ctx is cancelled or the device
	// fails. Each Update may carry only the variables that were read in that
	// cycle. Watch must not call sink after it returns. Returning nil or an
	// error before ctx is cancelled makes the core close the device and
	// reopen it with backoff.
	Watch(ctx context.Context, sink func(Update)) error

	// Commands lists the instant commands the device supports.
	Commands() []Command

	// Writable lists the variables SetVar accepts.
	Writable() []string

	// RunCommand runs an instant command. param is empty for most commands.
	RunCommand(ctx context.Context, name, param string) error

	// SetVar changes a writable variable.
	SetVar(ctx context.Context, name, value string) error

	// Close releases the device. The core calls it after Watch returns.
	Close() error
}

// Update carries variable values read from a device.
type Update struct {
	// Vars maps variable names to their current values.
	Vars map[string]string
	// Removed lists variables the device no longer reports.
	Removed []string
}
