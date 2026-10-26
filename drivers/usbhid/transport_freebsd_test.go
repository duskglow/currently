package usbhid

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestFreeBSDIoctls(t *testing.T) {
	if runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64" {
		// struct usb_gen_descriptor is 32 bytes on 64-bit platforms.
		if s := unsafe.Sizeof(genDescriptor{}); s != 32 {
			t.Errorf("genDescriptor is %d bytes, want 32", s)
		}
		for name, got := range map[string]uintptr{"USB_GET_REPORT_DESC": usbGetReportDesc,
			"USB_GET_REPORT": usbGetReport, "USB_SET_REPORT": usbSetReport} {
			want := map[string]uintptr{"USB_GET_REPORT_DESC": 0xc0205515, "USB_GET_REPORT": 0xc0205517,
				"USB_SET_REPORT": 0x80205518}[name]
			if got != want {
				t.Errorf("%s = %#x, want %#x", name, got, want)
			}
		}
	}
	kv := parsePnP(`vendor=0x0764 product=0x0601 devclass=0x00 sernum="CTHGP2001234" release=0x0001 mode=host intclass=0x03`)
	if kv["vendor"] != "0x0764" || kv["product"] != "0x0601" || kv["sernum"] != "CTHGP2001234" {
		t.Errorf("parsePnP = %v", kv)
	}
	if _, err := enumerate(); err != nil {
		t.Fatal(err)
	}
}
