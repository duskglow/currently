package usbhid

import "testing"

// TestEnumerateIOKit runs the IOKit enumeration through purego; on a
// machine without a UPS it must find nothing, not fail.
func TestEnumerateIOKit(t *testing.T) {
	devs, err := enumerate()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devs {
		t.Logf("found %s at %s, usage %s", d.Description(), d.Path, d.Usage.Name())
		if !d.IsPowerDevice() {
			t.Errorf("%s is not a power device; matching should only return UPSes", d.Description())
		}
	}
	if _, err := openDevice(DeviceInfo{Path: "IOService:0x1"}); err == nil {
		t.Error("opened a device that does not exist")
	}
}
