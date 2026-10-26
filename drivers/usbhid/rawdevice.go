package usbhid

import (
	"context"
	"fmt"
	"sync"

	"github.com/duskglow/currently/drivers/usbhid/hid"
)

// reportBackend reads and writes raw reports. buf[0] holds the report ID
// (0 when the device does not use report IDs, in which case the data
// starts at buf[1]).
type reportBackend interface {
	GetReport(t hid.ReportType, buf []byte) (int, error)
	SetFeature(buf []byte) error
	Close() error
}

// rawDevice implements Device for transports that expose the report
// descriptor, decoding reports with the hid package.
type rawDevice struct {
	mu      sync.Mutex
	info    DeviceInfo
	desc    *hid.Descriptor
	descRaw []byte
	be      reportBackend
	byPath  map[string]*hid.Field
	strings func(int) (string, error)
}

func newRawDevice(info DeviceInfo, descriptor []byte, be reportBackend) (*rawDevice, error) {
	d, err := hid.Parse(descriptor)
	if err != nil {
		return nil, fmt.Errorf("parse report descriptor of %s: %w", info.Description(), err)
	}
	var fields []hid.Field
	for _, f := range d.Fields {
		if f.Type != hid.Output {
			fields = append(fields, f)
		}
	}
	d.Fields = fields
	return &rawDevice{info: info, desc: d, descRaw: descriptor, be: be, byPath: fieldIndex(d.Fields)}, nil
}

func (d *rawDevice) Info() DeviceInfo    { return d.info }
func (d *rawDevice) Fields() []hid.Field { return d.desc.Fields }

// Descriptor returns the raw report descriptor, for captures.
func (d *rawDevice) Descriptor() []byte { return d.descRaw }

// buffer returns a buffer for a report, with the ID byte set. Unnumbered
// reports get a leading zero byte.
func (d *rawDevice) buffer(t hid.ReportType, id uint8) []byte {
	n := d.desc.ReportLength(t, id)
	if id == 0 {
		n++
	}
	buf := make([]byte, n)
	buf[0] = id
	return buf
}

func payload(buf []byte, id uint8) []byte {
	if id == 0 {
		return buf[1:]
	}
	return buf
}

func (d *rawDevice) Read(ctx context.Context) (map[string]int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	values := map[string]int64{}
	ok, failed := 0, 0
	var lastErr error
	for _, t := range []hid.ReportType{hid.Feature, hid.Input} {
		for _, id := range d.desc.ReportIDs(t) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			buf := d.buffer(t, id)
			n, err := d.be.GetReport(t, buf)
			if err != nil {
				failed++
				lastErr = err
				continue
			}
			ok++
			report := payload(buf[:max(n, 1)], id)
			for i := range d.desc.Fields {
				f := &d.desc.Fields[i]
				if f.Type == t && f.ReportID == id {
					if v, ok := f.Extract(report); ok {
						values[f.Path] = v
					}
				}
			}
		}
	}
	if ok == 0 && failed > 0 {
		return nil, fmt.Errorf("could not read any report from %s: %w", d.info.Description(), lastErr)
	}
	return values, nil
}

func (d *rawDevice) Write(path string, value int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.byPath[path]
	if !ok || f.Type != hid.Feature {
		return fmt.Errorf("%s is not a feature field", path)
	}
	buf := d.buffer(hid.Feature, f.ReportID)
	if _, err := d.be.GetReport(hid.Feature, buf); err != nil {
		return fmt.Errorf("read report %d before writing: %w", f.ReportID, err)
	}
	if err := f.Insert(payload(buf, f.ReportID), value); err != nil {
		return err
	}
	return d.be.SetFeature(buf)
}

func (d *rawDevice) String(index int) (string, error) {
	if d.strings == nil {
		return "", ErrNoStrings
	}
	return d.strings(index)
}

func (d *rawDevice) Close() error { return d.be.Close() }
