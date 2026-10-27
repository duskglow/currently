package usbhid

import (
	"context"
	"fmt"
	"maps"
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
	// errs holds the reports the last Read could not read, such as
	// "input report 8", with the error.
	errs map[string]string
	// longest is the longest report of each type, with its ID byte.
	longest map[hid.ReportType]int
	// raw holds the bytes of each report from the last Read, in hex.
	raw map[string]string
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
	longest := map[hid.ReportType]int{}
	for _, t := range []hid.ReportType{hid.Input, hid.Feature} {
		for _, id := range d.ReportIDs(t) {
			n := d.ReportLength(t, id)
			if id == 0 {
				n++
			}
			longest[t] = max(longest[t], n)
		}
	}
	return &rawDevice{info: info, desc: d, descRaw: descriptor, be: be, byPath: fieldIndex(d.Fields), longest: longest}, nil
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

// minReadBuffer is the smallest buffer a report is read into.
const minReadBuffer = 64

// readBuffer returns a buffer for reading a report: at least as long as
// the longest report of its type (as Windows does) and minReadBuffer. If
// firmware sends more than the descriptor declares, a buffer of the
// declared length overflows and the read fails; extra room is harmless.
func (d *rawDevice) readBuffer(t hid.ReportType, id uint8) []byte {
	buf := d.buffer(t, id)
	if n := max(d.longest[t], minReadBuffer); n > len(buf) {
		buf = append(buf, make([]byte, n-len(buf))...)
	}
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
	d.errs = map[string]string{}
	d.raw = map[string]string{}
	ok, failed := 0, 0
	var lastErr error
	for _, t := range []hid.ReportType{hid.Feature, hid.Input} {
		for _, id := range d.desc.ReportIDs(t) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			buf := d.readBuffer(t, id)
			n, err := d.be.GetReport(t, buf)
			if err != nil {
				failed++
				lastErr = err
				d.errs[fmt.Sprintf("%s report %d", t, id)] = err.Error()
				continue
			}
			ok++
			d.raw[fmt.Sprintf("%s report %d", t, id)] = fmt.Sprintf("%x (%d bytes)", buf[:max(n, 0)], n)
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

// RawReports returns the bytes of each report from the last Read, in hex,
// for captures.
func (d *rawDevice) RawReports() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return maps.Clone(d.raw)
}

// ReportErrors returns the reports the last Read could not read, with
// their errors.
func (d *rawDevice) ReportErrors() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return maps.Clone(d.errs)
}

func (d *rawDevice) Write(path string, value int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.byPath[path]
	if !ok || f.Type != hid.Feature {
		return fmt.Errorf("%s is not a feature field", path)
	}
	exact := len(d.buffer(hid.Feature, f.ReportID))
	buf := d.readBuffer(hid.Feature, f.ReportID)
	if _, err := d.be.GetReport(hid.Feature, buf); err != nil {
		return fmt.Errorf("read report %d before writing: %w", f.ReportID, err)
	}
	buf = buf[:exact] // the report as the descriptor declares it
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
