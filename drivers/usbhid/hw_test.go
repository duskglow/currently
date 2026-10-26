package usbhid

import (
	"context"
	"os"
	"sort"
	"testing"
)

// TestHardware reads every UPS attached to this machine and logs its fields
// and values. It runs only with NUTNG_HW=1:
//
//	NUTNG_HW=1 go test ./drivers/usbhid -run TestHardware -v
func TestHardware(t *testing.T) {
	if os.Getenv("NUTNG_HW") == "" {
		t.Skip("set NUTNG_HW=1 to test against attached UPS hardware")
	}
	all, err := enumerate()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, info := range all {
		if !info.IsPowerDevice() {
			continue
		}
		found++
		t.Logf("device: %s path=%s usage=%s", info.Description(), info.Path, info.Usage.Name())
		dev, err := openDevice(info)
		if err != nil {
			t.Errorf("open: %v", err)
			continue
		}
		values, err := dev.Read(context.Background())
		if err != nil {
			t.Errorf("read: %v", err)
		}
		fields := dev.Fields()
		sort.Slice(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })
		for _, f := range fields {
			v, ok := values[f.Path]
			t.Logf("  %-7s id=%02x %-60s raw=%-6v ok=%-5v lmin=%d lmax=%d unit=%08x exp=%d flag=%v",
				f.Type, f.ReportID, f.Path, v, ok, f.LogicalMin, f.LogicalMax, f.Unit, f.UnitExponent, f.Flag)
		}
		dev.Close()
	}
	if found == 0 {
		t.Skip("no UPS attached")
	}
}
