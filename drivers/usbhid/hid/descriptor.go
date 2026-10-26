// Package hid parses HID report descriptors and decodes report values, as
// needed for UPSes that follow the USB HID Power Device class.
package hid

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ReportType is the kind of HID report.
type ReportType uint8

const (
	Input   ReportType = 1
	Output  ReportType = 2
	Feature ReportType = 3
)

func (t ReportType) String() string {
	switch t {
	case Input:
		return "input"
	case Output:
		return "output"
	case Feature:
		return "feature"
	}
	return "report(" + strconv.Itoa(int(t)) + ")"
}

// Field is one value in a report, such as UPS.PowerSummary.RemainingCapacity
// in feature report 0x0c.
type Field struct {
	// Path names the field by its usage and the usages of the collections
	// around it, for example "UPS.PowerSummary.PresentStatus.ACPresent".
	// Repeated sibling collections get an index: "UPS.Outlet[2].Present".
	Path     string     `json:"path"`
	Usage    Usage      `json:"usage"`
	Type     ReportType `json:"type"`
	ReportID uint8      `json:"report_id"`
	// BitOffset is the position after the report ID byte, if any. It is -1
	// when unknown (fields read through the Windows HID API).
	BitOffset   int    `json:"bit_offset"`
	BitSize     int    `json:"bit_size"`
	LogicalMin  int32  `json:"logical_min"`
	LogicalMax  int32  `json:"logical_max"`
	PhysicalMin int32  `json:"physical_min,omitempty"`
	PhysicalMax int32  `json:"physical_max,omitempty"`
	Unit        uint32 `json:"unit,omitempty"`
	// UnitExponent is the base-10 exponent from the descriptor, -8 to 7.
	UnitExponent int8 `json:"unit_exponent,omitempty"`
	// Flag is set for 1-bit on/off fields such as PresentStatus.Charging.
	Flag bool `json:"flag,omitempty"`

	// Collection is the OS link collection, used by the Windows transport.
	Collection uint16 `json:"-"`
}

// Extract reads the field's raw (logical) value from a report. The report
// starts with the report ID byte when the field has a report ID.
func (f *Field) Extract(report []byte) (int64, bool) {
	if f.BitOffset < 0 || f.BitSize <= 0 || f.BitSize > 32 {
		return 0, false
	}
	off := f.BitOffset
	if f.ReportID != 0 {
		if len(report) == 0 || report[0] != f.ReportID {
			return 0, false
		}
		off += 8
	}
	if (off+f.BitSize+7)/8 > len(report) {
		return 0, false
	}
	var v uint64
	for i := 0; i < f.BitSize; i++ {
		bit := off + i
		if report[bit/8]&(1<<(bit%8)) != 0 {
			v |= 1 << i
		}
	}
	return f.signExtend(v), true
}

func (f *Field) signExtend(v uint64) int64 {
	if f.LogicalMin < 0 && f.BitSize < 64 && v&(1<<(f.BitSize-1)) != 0 {
		return int64(v) - int64(1)<<f.BitSize
	}
	return int64(v)
}

// SignExtend interprets the low BitSize bits of v as the field's raw value.
func (f *Field) SignExtend(v uint32) int64 { return f.signExtend(uint64(v)) }

// Insert writes a raw value into a report buffer.
func (f *Field) Insert(report []byte, v int64) error {
	if f.BitOffset < 0 {
		return errors.New("field position unknown")
	}
	off := f.BitOffset
	if f.ReportID != 0 {
		off += 8
		if len(report) > 0 {
			report[0] = f.ReportID
		}
	}
	if (off+f.BitSize+7)/8 > len(report) {
		return fmt.Errorf("report too short for %s", f.Path)
	}
	u := uint64(v)
	for i := 0; i < f.BitSize; i++ {
		bit := off + i
		if u&(1<<i) != 0 {
			report[bit/8] |= 1 << (bit % 8)
		} else {
			report[bit/8] &^= 1 << (bit % 8)
		}
	}
	return nil
}

// Exponent returns the power of ten to apply to the physical value. The
// HID Power Device class expresses volts and watts in the cgs system,
// where an exponent of 7 means plain volts or watts; many devices instead
// leave the exponent at 0 for plain units, so 0 is taken as-is.
func (f *Field) Exponent() int {
	exp := int(f.UnitExponent)
	if exp != 0 && (f.Unit == UnitVolt || f.Unit == UnitWatt) {
		exp -= 7
	}
	return exp
}

// Physical converts a raw value to physical units (volts, seconds, ...).
func (f *Field) Physical(raw int64) float64 {
	v := float64(raw)
	if f.PhysicalMin != f.PhysicalMax && f.LogicalMin != f.LogicalMax &&
		(f.PhysicalMin != f.LogicalMin || f.PhysicalMax != f.LogicalMax) {
		v = (v-float64(f.LogicalMin))*float64(f.PhysicalMax-f.PhysicalMin)/
			float64(f.LogicalMax-f.LogicalMin) + float64(f.PhysicalMin)
	}
	return v * math.Pow10(f.Exponent())
}

// Raw converts a physical value back to a raw value, clamped to the
// logical range.
func (f *Field) Raw(physical float64) int64 {
	v := physical / math.Pow10(f.Exponent())
	if f.PhysicalMin != f.PhysicalMax && f.LogicalMin != f.LogicalMax &&
		(f.PhysicalMin != f.LogicalMin || f.PhysicalMax != f.LogicalMax) {
		v = (v-float64(f.PhysicalMin))*float64(f.LogicalMax-f.LogicalMin)/
			float64(f.PhysicalMax-f.PhysicalMin) + float64(f.LogicalMin)
	}
	r := int64(math.Round(v))
	if f.LogicalMin < f.LogicalMax {
		r = max(int64(f.LogicalMin), min(int64(f.LogicalMax), r))
	}
	return r
}

// Descriptor is a parsed report descriptor.
type Descriptor struct {
	// Usage of the first top-level (application) collection, such as
	// UsageUPS.
	Usage  Usage
	Fields []Field
	// lengths maps report type and ID to the report length in bytes,
	// including the report ID byte.
	lengths map[reportKey]int
}

type reportKey struct {
	t  ReportType
	id uint8
}

// ReportLength returns the length in bytes of a report, including the
// report ID byte when IDs are used, or 0 if the report does not exist.
func (d *Descriptor) ReportLength(t ReportType, id uint8) int { return d.lengths[reportKey{t, id}] }

// ReportIDs returns the IDs of the reports of type t, sorted.
func (d *Descriptor) ReportIDs(t ReportType) []uint8 {
	var ids []uint8
	for k := range d.lengths {
		if k.t == t {
			ids = append(ids, k.id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// collection is a node of the collection tree built while parsing.
type collection struct {
	usage    Usage
	parent   *collection
	children []*collection
	name     string // path element, set after parsing
}

type globals struct {
	usagePage   uint16
	logicalMin  int32
	logicalMax  int32
	logMaxRaw   uint32
	physicalMin int32
	physicalMax int32
	physMaxRaw  uint32
	unitExp     int8
	unit        uint32
	reportSize  int
	reportCount int
	reportID    uint8
}

type locals struct {
	usages   []Usage
	min, max Usage
	hasMin   bool
	hasMax   bool
}

// maxFields guards against hostile descriptors that declare huge reports.
const maxFields = 4096

// Parse parses a HID report descriptor.
func Parse(data []byte) (*Descriptor, error) {
	d := &Descriptor{lengths: map[reportKey]int{}}
	var (
		g       globals
		stack   []globals
		l       locals
		root    = &collection{}
		cur     = root
		offsets = map[reportKey]int{}
		owners  []*collection // collection of each field, parallel to d.Fields
	)

	for i := 0; i < len(data); {
		prefix := data[i]
		if prefix == 0xFE { // long item: skip
			if i+1 >= len(data) {
				return nil, errors.New("truncated long item")
			}
			i += 3 + int(data[i+1])
			continue
		}
		size := int(prefix & 3)
		if size == 3 {
			size = 4
		}
		if i+1+size > len(data) {
			return nil, fmt.Errorf("item at byte %d runs past the end of the descriptor", i)
		}
		var u uint32
		for b := 0; b < size; b++ {
			u |= uint32(data[i+1+b]) << (8 * b)
		}
		s := signed(u, size)
		typ, tag := (prefix>>2)&3, prefix>>4
		i += 1 + size

		switch typ {
		case 0: // main
			switch tag {
			case 0x8, 0x9, 0xB: // input, output, feature
				rt := map[byte]ReportType{0x8: Input, 0x9: Output, 0xB: Feature}[tag]
				key := reportKey{rt, g.reportID}
				start := offsets[key]
				total := g.reportSize * g.reportCount
				if g.reportSize < 0 || g.reportCount < 0 || total > 1<<16 {
					return nil, fmt.Errorf("unreasonable report size %d × %d", g.reportSize, g.reportCount)
				}
				constant, variable := u&1 != 0, u&2 != 0
				if !constant && variable {
					usages := expand(l)
					for n := 0; n < g.reportCount && len(usages) > 0; n++ {
						if len(d.Fields) >= maxFields {
							return nil, errors.New("too many fields")
						}
						us := usages[min(n, len(usages)-1)]
						f := Field{
							Usage: us, Type: rt, ReportID: g.reportID,
							BitOffset: start + n*g.reportSize, BitSize: g.reportSize,
							LogicalMin: g.logicalMin, LogicalMax: fixMax(g.logicalMin, g.logicalMax, g.logMaxRaw),
							PhysicalMin: g.physicalMin, PhysicalMax: fixMax(g.physicalMin, g.physicalMax, g.physMaxRaw),
							Unit: g.unit, UnitExponent: g.unitExp,
						}
						f.Flag = f.BitSize == 1
						d.Fields = append(d.Fields, f)
						owners = append(owners, cur)
					}
				}
				offsets[key] = start + total
				length := (offsets[key] + 7) / 8
				if g.reportID != 0 {
					length++
				}
				d.lengths[key] = max(d.lengths[key], length)
			case 0xA: // collection
				us := Usage(0)
				switch {
				case len(l.usages) > 0:
					us = l.usages[0]
				case l.hasMin:
					us = l.min
				}
				c := &collection{usage: us, parent: cur}
				cur.children = append(cur.children, c)
				cur = c
				if c.parent == root && d.Usage == 0 {
					d.Usage = us
				}
			case 0xC: // end collection
				if cur.parent == nil {
					return nil, errors.New("end collection without a collection")
				}
				cur = cur.parent
			}
			l = locals{}
		case 1: // global
			switch tag {
			case 0x0:
				g.usagePage = uint16(u)
			case 0x1:
				g.logicalMin = s
			case 0x2:
				g.logicalMax, g.logMaxRaw = s, u
			case 0x3:
				g.physicalMin = s
			case 0x4:
				g.physicalMax, g.physMaxRaw = s, u
			case 0x5:
				g.unitExp = unitExponent(u, size)
			case 0x6:
				g.unit = u
			case 0x7:
				g.reportSize = int(u)
			case 0x8:
				if u == 0 || u > 255 {
					return nil, fmt.Errorf("invalid report ID %d", u)
				}
				g.reportID = uint8(u)
			case 0x9:
				g.reportCount = int(u)
			case 0xA:
				stack = append(stack, g)
			case 0xB:
				if len(stack) == 0 {
					return nil, errors.New("pop without push")
				}
				g, stack = stack[len(stack)-1], stack[:len(stack)-1]
			}
		case 2: // local
			usage := func() Usage {
				if size == 4 {
					return Usage(u)
				}
				return NewUsage(g.usagePage, uint16(u))
			}
			switch tag {
			case 0x0:
				if len(l.usages) < maxFields {
					l.usages = append(l.usages, usage())
				}
			case 0x1:
				l.min, l.hasMin = usage(), true
			case 0x2:
				l.max, l.hasMax = usage(), true
			}
		}
	}
	if cur != root {
		return nil, errors.New("collection not closed")
	}

	nameCollections(root)
	for i := range d.Fields {
		d.Fields[i].Path = path(owners[i], d.Fields[i].Usage)
	}
	return d, nil
}

// expand returns the usages declared before a main item: the listed
// usages followed by the usage range, if any.
func expand(l locals) []Usage {
	us := l.usages
	if l.hasMin && l.hasMax && l.max >= l.min && l.max-l.min < maxFields {
		for u := l.min; u <= l.max; u++ {
			us = append(us, u)
		}
	}
	return us
}

// signed sign-extends an item's data.
func signed(u uint32, size int) int32 {
	switch size {
	case 1:
		return int32(int8(u))
	case 2:
		return int32(int16(u))
	default:
		return int32(u)
	}
}

// fixMax reads a maximum as unsigned when the minimum is not negative: many
// descriptors write 255 as the single byte 0xFF, which is -1 when signed.
func fixMax(lo, hi int32, raw uint32) int32 {
	if lo >= 0 && hi < 0 && raw <= math.MaxInt32 {
		return int32(raw)
	}
	return hi
}

// unitExponent decodes the Unit Exponent item: a 4-bit signed nibble in a
// one-byte item, or a full signed value from devices that ignore that.
func unitExponent(u uint32, size int) int8 {
	if size <= 1 && u <= 0xF {
		if u > 7 {
			return int8(u) - 16
		}
		return int8(u)
	}
	return int8(signed(u, size))
}

// nameCollections sets each collection's path element. Siblings that share
// a usage are numbered from 1, e.g. Outlet[1], Outlet[2].
func nameCollections(c *collection) {
	counts := map[Usage]int{}
	for _, ch := range c.children {
		counts[ch.usage]++
	}
	seen := map[Usage]int{}
	for _, ch := range c.children {
		ch.name = ch.usage.Name()
		if counts[ch.usage] > 1 {
			seen[ch.usage]++
			ch.name += "[" + strconv.Itoa(seen[ch.usage]) + "]"
		}
		nameCollections(ch)
	}
}

func path(c *collection, u Usage) string {
	var parts []string
	for ; c != nil && c.parent != nil; c = c.parent {
		parts = append(parts, c.name)
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(append(parts, u.Name()), ".")
}
