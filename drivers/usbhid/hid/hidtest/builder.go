// Package hidtest builds HID report descriptors and reports for tests.
package hidtest

import "github.com/nut-ng/nut-ng/drivers/usbhid/hid"

// Builder writes report descriptor items.
type Builder struct{ b []byte }

// Bytes returns the descriptor.
func (b *Builder) Bytes() []byte { return b.b }

func (b *Builder) item(typ, tag byte, v int64) *Builder {
	var data []byte
	switch {
	case v == 0:
	case v >= -128 && v <= 127:
		data = []byte{byte(v)}
	case v >= -32768 && v <= 32767:
		data = []byte{byte(v), byte(v >> 8)}
	default:
		data = []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
	}
	size := byte(len(data))
	if size == 4 {
		size = 3
	}
	b.b = append(b.b, tag<<4|typ<<2|size)
	b.b = append(b.b, data...)
	return b
}

// uitem writes an unsigned item (so 255 is one byte, not two).
func (b *Builder) uitem(typ, tag byte, v uint32) *Builder {
	switch {
	case v == 0:
		b.b = append(b.b, tag<<4|typ<<2)
	case v <= 0xFF:
		b.b = append(b.b, tag<<4|typ<<2|1, byte(v))
	case v <= 0xFFFF:
		b.b = append(b.b, tag<<4|typ<<2|2, byte(v), byte(v>>8))
	default:
		b.b = append(b.b, tag<<4|typ<<2|3, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	}
	return b
}

func (b *Builder) UsagePage(p uint16) *Builder   { return b.uitem(1, 0x0, uint32(p)) }
func (b *Builder) Usage(id uint16) *Builder      { return b.uitem(2, 0x0, uint32(id)) }
func (b *Builder) LogicalMin(v int32) *Builder   { return b.item(1, 0x1, int64(v)) }
func (b *Builder) LogicalMax(v int32) *Builder   { return b.item(1, 0x2, int64(v)) }
func (b *Builder) Unit(u uint32) *Builder        { return b.uitem(1, 0x6, u) }
func (b *Builder) ReportSize(n uint32) *Builder  { return b.uitem(1, 0x7, n) }
func (b *Builder) ReportID(id uint8) *Builder    { return b.uitem(1, 0x8, uint32(id)) }
func (b *Builder) ReportCount(n uint32) *Builder { return b.uitem(1, 0x9, n) }
func (b *Builder) Feature(flags uint32) *Builder { return b.uitem(0, 0xB, flags) }
func (b *Builder) Input(flags uint32) *Builder   { return b.uitem(0, 0x8, flags) }
func (b *Builder) Collection(kind byte) *Builder { return b.uitem(0, 0xA, uint32(kind)) }
func (b *Builder) EndCollection() *Builder       { return b.uitem(0, 0xC, 0) }
func (b *Builder) UnitExponent(e int8) *Builder  { return b.uitem(1, 0x5, uint32(uint8(e))&0xF) }
func (b *Builder) PhysicalMin(v int32) *Builder  { return b.item(1, 0x3, int64(v)) }
func (b *Builder) PhysicalMax(v int32) *Builder  { return b.item(1, 0x4, int64(v)) }

// Data/variable/absolute flags for Input and Feature items.
const (
	Var   = 0x02
	Const = 0x01
)

// Value adds one variable field in its own report: a collection-less
// shorthand for the common UPS layout. It has no physical range.
func (b *Builder) Value(page, usage uint16, id uint8, bits uint32, lmin, lmax int32, unit uint32, exp int8) *Builder {
	return b.Scaled(page, usage, id, bits, lmin, lmax, 0, 0, unit, exp)
}

// Scaled is Value with a physical range.
func (b *Builder) Scaled(page, usage uint16, id uint8, bits uint32, lmin, lmax, pmin, pmax int32, unit uint32, exp int8) *Builder {
	return b.ReportID(id).UsagePage(page).Usage(usage).
		LogicalMin(lmin).LogicalMax(lmax).PhysicalMin(pmin).PhysicalMax(pmax).Unit(unit).UnitExponent(exp).
		ReportSize(bits).ReportCount(1).Feature(Var)
}

// Flags adds 1-bit fields to feature report id, padded to a whole byte.
func (b *Builder) Flags(id uint8, page uint16, usages ...uint16) *Builder {
	b.ReportID(id).UsagePage(page)
	for _, u := range usages {
		b.Usage(u)
	}
	b.LogicalMin(0).LogicalMax(1).Unit(0).UnitExponent(0).ReportSize(1).ReportCount(uint32(len(usages))).Feature(Var)
	if pad := (8 - len(usages)%8) % 8; pad > 0 {
		b.ReportCount(uint32(pad)).Feature(Const)
	}
	return b
}

// Reports builds report buffers from raw values by field path.
func Reports(d *hid.Descriptor, t hid.ReportType, values map[string]int64) map[uint8][]byte {
	out := map[uint8][]byte{}
	for _, id := range d.ReportIDs(t) {
		out[id] = make([]byte, d.ReportLength(t, id))
	}
	for i := range d.Fields {
		f := &d.Fields[i]
		if f.Type != t {
			continue
		}
		if v, ok := values[f.Path]; ok {
			_ = f.Insert(out[f.ReportID], v)
		} else if f.ReportID != 0 {
			out[f.ReportID][0] = f.ReportID
		}
	}
	return out
}

// CyberPower returns a descriptor laid out like a CyberPower CP1500PFCLCD
// (USB 0764:0601), as read from real hardware, and raw values for it on
// line power with a full battery.
func CyberPower() ([]byte, map[string]int64) {
	const (
		pd = hid.PagePowerDevice
		bs = hid.PageBatterySystem
	)
	b := &Builder{}
	b.UsagePage(pd).Usage(0x04).Collection(1) // UPS
	b.Usage(0x24).Collection(0)               // PowerSummary
	b.Value(bs, 0x66, 0x08, 8, 0, 100, 0, 0)  // RemainingCapacity
	b.Value(bs, 0x68, 0x08, 16, 0, 65535, hid.UnitSecond, 0)
	b.Value(bs, 0x2a, 0x08, 16, 0, 600, hid.UnitSecond, 0)
	b.Value(bs, 0x29, 0x07, 8, 0, 100, 0, 0)
	b.Value(bs, 0x8c, 0x07, 8, 0, 100, 0, 0)
	b.Value(pd, 0x30, 0x0a, 16, 0, 480, hid.UnitVolt, 6) // battery voltage, decivolts
	b.Value(pd, 0x40, 0x09, 16, 0, 480, hid.UnitVolt, 6)
	b.Value(pd, 0x5a, 0x0c, 8, 1, 3, 0, 0) // AudibleAlarmControl
	b.Value(bs, 0x89, 0x03, 8, 0, 255, 0, 0)
	b.UsagePage(pd).Usage(0x02).Collection(2) // PresentStatus
	b.Flags(0x0b, bs, 0xd0, 0x42, 0x44, 0x45, 0x46, 0x43)
	b.EndCollection()
	b.EndCollection()
	b.UsagePage(pd).Usage(0x1a).Collection(0) // Input
	b.Value(pd, 0x30, 0x0f, 8, 0, 255, hid.UnitVolt, 7)
	b.Value(pd, 0x40, 0x0e, 8, 0, 255, hid.UnitVolt, 7)
	b.EndCollection()
	b.UsagePage(pd).Usage(0x1c).Collection(0)             // Output
	b.Value(pd, 0x30, 0x12, 8, 135, 144, hid.UnitVolt, 7) // ranges are wrong on the real device
	b.Value(pd, 0x35, 0x13, 8, 0, 255, 0, 0)
	b.Value(pd, 0x34, 0x19, 16, 0, 2000, hid.UnitWatt, 7)
	b.Value(pd, 0x33, 0x1d, 16, 0, 2000, hid.UnitWatt, 7)
	b.Value(pd, 0x44, 0x18, 16, 0, 2000, hid.UnitWatt, 7)
	b.Value(pd, 0x58, 0x14, 8, 0, 6, 0, 0) // Test
	// Delays count in minutes: the physical range is 60 times the logical.
	b.Scaled(pd, 0x57, 0x15, 16, -1, 32767, -60, 1966020, hid.UnitSecond, 0)
	b.Scaled(pd, 0x56, 0x16, 16, -1, 32767, -60, 1966020, hid.UnitSecond, 0)
	b.Value(pd, 0x53, 0x10, 8, 97, 103, hid.UnitVolt, 7)
	b.Flags(0x17, pd, 0x65, 0x6e) // Overload, Boost
	b.EndCollection()
	b.EndCollection()

	return b.Bytes(), map[string]int64{
		"UPS.PowerSummary.RemainingCapacity":                         100,
		"UPS.PowerSummary.RunTimeToEmpty":                            1150,
		"UPS.PowerSummary.RemainingTimeLimit":                        300,
		"UPS.PowerSummary.RemainingCapacityLimit":                    10,
		"UPS.PowerSummary.WarningCapacityLimit":                      20,
		"UPS.PowerSummary.Voltage":                                   274,
		"UPS.PowerSummary.ConfigVoltage":                             240,
		"UPS.PowerSummary.AudibleAlarmControl":                       2,
		"UPS.PowerSummary.iDeviceChemistry":                          4,
		"UPS.PowerSummary.PresentStatus.ACPresent":                   1,
		"UPS.PowerSummary.PresentStatus.BelowRemainingCapacityLimit": 0,
		"UPS.PowerSummary.PresentStatus.Charging":                    0,
		"UPS.PowerSummary.PresentStatus.Discharging":                 0,
		"UPS.PowerSummary.PresentStatus.FullyCharged":                1,
		"UPS.PowerSummary.PresentStatus.RemainingTimeLimitExpired":   0,
		"UPS.Input.Voltage":                                          123,
		"UPS.Input.ConfigVoltage":                                    120,
		"UPS.Output.Voltage":                                         123,
		"UPS.Output.PercentLoad":                                     37,
		"UPS.Output.ActivePower":                                     378,
		"UPS.Output.ApparentPower":                                   379,
		"UPS.Output.ConfigActivePower":                               1000,
		"UPS.Output.Test":                                            6,
		"UPS.Output.DelayBeforeShutdown":                             -1,
		"UPS.Output.DelayBeforeStartup":                              -1,
		"UPS.Output.LowVoltageTransfer":                              100,
		"UPS.Output.Overload":                                        0,
		"UPS.Output.Boost":                                           0,
	}
}
