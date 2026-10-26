//go:build !linux && !windows

package usbhid

import (
	"fmt"
	"runtime"
)

// macOS (IOKit) and FreeBSD (hidraw/uhid) transports come in a later
// milestone. Captures can still be replayed with the replay driver.

func enumerate() ([]DeviceInfo, error) { return nil, nil }

func openDevice(info DeviceInfo) (Device, error) {
	return nil, fmt.Errorf("USB UPS access is not supported on %s yet", runtime.GOOS)
}
