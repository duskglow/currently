package usbhid

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindNodeByNumber finds a device node by its number under another
// name, as a container gets a UPS passed in as /dev/ups-rack. /dev/null
// (1:3) stands in for a hidraw device.
func TestFindNodeByNumber(t *testing.T) {
	if !sameDevice("/dev/null", "1:3") {
		t.Skip("/dev/null is not character device 1:3 here")
	}
	if sameDevice("/dev/null", "1:5") || sameDevice(t.TempDir(), "") {
		t.Error("sameDevice matched the wrong device")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "ups")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", filepath.Join(sub, "rack")); err != nil {
		t.Fatal(err)
	}
	if got := findNode(dir, "1:3", 2); got != filepath.Join(sub, "rack") {
		t.Errorf("findNode = %q", got)
	}
	if got := findNode(dir, "1:3", 1); got != "" {
		t.Errorf("findNode searched too deep: %q", got)
	}
	if got := findNode(dir, "1:9", 2); got != "" {
		t.Errorf("findNode found %q for a missing device", got)
	}
}
