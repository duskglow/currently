package usbhid

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/duskglow/currently/drivers/usbhid/hid"
)

// DeviceInfo describes a HID device found by enumeration.
type DeviceInfo struct {
	// Path is the OS path used to open the device (/dev/hidraw0,
	// \\?\hid#vid_...).
	Path         string `json:"path"`
	VendorID     uint16 `json:"vendor_id"`
	ProductID    uint16 `json:"product_id"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Product      string `json:"product,omitempty"`
	Serial       string `json:"serial,omitempty"`
	// Usage of the device's top-level collection, such as hid.UsageUPS.
	Usage hid.Usage `json:"usage"`
	// Problem explains why the device cannot be opened, such as a device
	// node missing in a container; empty when it can be.
	Problem string `json:"problem,omitempty"`
}

// IsPowerDevice reports whether the device's top-level collection belongs
// to the HID Power Device or Battery System pages, as UPSes do.
func (d DeviceInfo) IsPowerDevice() bool {
	p := d.Usage.Page()
	return p == hid.PagePowerDevice || p == hid.PageBatterySystem
}

// ID returns "vvvv:pppp".
func (d DeviceInfo) ID() string { return fmt.Sprintf("%04x:%04x", d.VendorID, d.ProductID) }

// Name returns the manufacturer and product, such as "CPS CP1500PFCLCDa".
func (d DeviceInfo) Name() string {
	name := strings.TrimSpace(d.Manufacturer + " " + d.Product)
	if d.Manufacturer != "" && strings.HasPrefix(d.Product, d.Manufacturer) {
		name = d.Product
	}
	if name == "" {
		name = "USB UPS"
	}
	return name
}

// Description returns the name with the USB ID and serial number.
func (d DeviceInfo) Description() string {
	s := fmt.Sprintf("%s (%s", d.Name(), d.ID())
	if d.Serial != "" {
		s += ", serial " + d.Serial
	}
	return s + ")"
}

// Device is an open HID device. Values are keyed by field path.
type Device interface {
	Info() DeviceInfo
	// Fields lists the device's input and feature fields.
	Fields() []hid.Field
	// Read reads every input and feature report and returns raw values by
	// path. Reports that fail to read are skipped; Read fails only when no
	// report could be read.
	Read(ctx context.Context) (map[string]int64, error)
	// Write sets a feature field to a raw value.
	Write(path string, value int64) error
	// String returns a USB string descriptor by index.
	String(index int) (string, error)
	Close() error
}

// ErrNoStrings means the transport cannot read USB strings by index.
var ErrNoStrings = errors.New("USB strings by index are not available on this platform")

// fieldIndex finds feature fields by path, preferring feature reports
// because they can be both read and written.
func fieldIndex(fields []hid.Field) map[string]*hid.Field {
	idx := map[string]*hid.Field{}
	for i := range fields {
		f := &fields[i]
		if old, ok := idx[f.Path]; !ok || (old.Type != hid.Feature && f.Type == hid.Feature) {
			idx[f.Path] = f
		}
	}
	return idx
}
