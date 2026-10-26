package hid_test

import (
	"math"
	"testing"

	"github.com/duskglow/currently/drivers/usbhid/hid"
	"github.com/duskglow/currently/drivers/usbhid/hid/hidtest"
)

func field(t *testing.T, d *hid.Descriptor, path string) *hid.Field {
	t.Helper()
	for i := range d.Fields {
		if d.Fields[i].Path == path {
			return &d.Fields[i]
		}
	}
	t.Fatalf("no field %s", path)
	return nil
}

func TestParseCyberPower(t *testing.T) {
	desc, values := hidtest.CyberPower()
	d, err := hid.Parse(desc)
	if err != nil {
		t.Fatal(err)
	}
	if d.Usage != hid.UsageUPS {
		t.Errorf("top-level usage %s", d.Usage.Name())
	}
	for path := range values {
		field(t, d, path)
	}

	rc := field(t, d, "UPS.PowerSummary.RemainingCapacity")
	if rc.ReportID != 0x08 || rc.BitOffset != 0 || rc.BitSize != 8 || rc.LogicalMax != 100 {
		t.Errorf("RemainingCapacity = %+v", rc)
	}
	rt := field(t, d, "UPS.PowerSummary.RunTimeToEmpty")
	if rt.ReportID != 0x08 || rt.BitOffset != 8 || rt.BitSize != 16 || rt.LogicalMax != 65535 {
		t.Errorf("RunTimeToEmpty = %+v (65535 must not read as -1)", rt)
	}
	if got := d.ReportLength(hid.Feature, 0x08); got != 1+1+2+2 {
		t.Errorf("report 8 length = %d", got)
	}
	ac := field(t, d, "UPS.PowerSummary.PresentStatus.ACPresent")
	if !ac.Flag || ac.ReportID != 0x0b || ac.BitOffset != 0 {
		t.Errorf("ACPresent = %+v", ac)
	}
	if got := d.ReportLength(hid.Feature, 0x0b); got != 2 {
		t.Errorf("flag report length = %d, want 2", got)
	}
	delay := field(t, d, "UPS.Output.DelayBeforeShutdown")
	if delay.LogicalMin != -1 || delay.LogicalMax != 32767 {
		t.Errorf("DelayBeforeShutdown range %d..%d", delay.LogicalMin, delay.LogicalMax)
	}

	reports := hidtest.Reports(d, hid.Feature, values)
	for path, want := range values {
		f := field(t, d, path)
		got, ok := f.Extract(reports[f.ReportID])
		if !ok || got != want {
			t.Errorf("%s: extracted %d (ok=%v), want %d", path, got, ok, want)
		}
	}
}

func TestPhysical(t *testing.T) {
	desc, _ := hidtest.CyberPower()
	d, _ := hid.Parse(desc)
	tests := []struct {
		path string
		raw  int64
		want float64
	}{
		{"UPS.Input.Voltage", 123, 123},         // volts, exponent 7
		{"UPS.PowerSummary.Voltage", 274, 27.4}, // decivolts, exponent 6
		{"UPS.Output.ActivePower", 378, 378},    // watts, exponent 7
		{"UPS.PowerSummary.RunTimeToEmpty", 1150, 1150},
	}
	for _, tt := range tests {
		f := field(t, d, tt.path)
		if got := f.Physical(tt.raw); math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("%s: Physical(%d) = %v, want %v", tt.path, tt.raw, got, tt.want)
		}
		if back := f.Raw(tt.want); back != tt.raw {
			t.Errorf("%s: Raw(%v) = %d, want %d", tt.path, tt.want, back, tt.raw)
		}
	}
}

func TestSiblingIndex(t *testing.T) {
	b := &hidtest.Builder{}
	b.UsagePage(hid.PagePowerDevice).Usage(0x04).Collection(1)
	b.Usage(0x16).Collection(0) // PowerConverter
	for i, id := range []uint8{1, 2} {
		b.UsagePage(hid.PagePowerDevice).Usage(0x1a).Collection(0) // Input
		b.Value(hid.PagePowerDevice, 0x30, id, 8, 0, 255, hid.UnitVolt, 7)
		b.EndCollection()
		_ = i
	}
	b.EndCollection().EndCollection()
	d, err := hid.Parse(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	field(t, d, "UPS.PowerConverter.Input[1].Voltage")
	field(t, d, "UPS.PowerConverter.Input[2].Voltage")
}

func TestUnknownUsageName(t *testing.T) {
	if got := hid.NewUsage(0xff86, 0x16).Name(); got != "ff860016" {
		t.Errorf("got %s", got)
	}
}

func TestParseErrors(t *testing.T) {
	for name, desc := range map[string][]byte{
		"truncated":   {0x26, 0xff},
		"unclosed":    {0x05, 0x84, 0x09, 0x04, 0xa1, 0x01},
		"extra end":   {0xc0},
		"pop":         {0xb4},
		"report id 0": {0x85, 0x00},
		"huge report": {0x75, 0xff, 0x96, 0xff, 0xff, 0xb1, 0x02},
	} {
		if _, err := hid.Parse(desc); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func FuzzParse(f *testing.F) {
	desc, _ := hidtest.CyberPower()
	f.Add(desc)
	f.Add([]byte{0x05, 0x84, 0x09, 0x04, 0xa1, 0x01, 0xc0})
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := hid.Parse(data)
		if err != nil {
			return
		}
		for i := range d.Fields {
			fl := &d.Fields[i]
			buf := make([]byte, d.ReportLength(fl.Type, fl.ReportID))
			if fl.ReportID != 0 && len(buf) > 0 {
				buf[0] = fl.ReportID
			}
			fl.Extract(buf)
			_ = fl.Insert(buf, 1)
			fl.Physical(1)
		}
	})
}
