//go:build !linux && !windows && !darwin && !freebsd

package usbhid

import (
	"fmt"
	"runtime"
)

// No USB transport on this platform (OpenBSD, NetBSD, ...). Captures can
// still be replayed with the replay driver.

func enumerate() ([]DeviceInfo, error) { return nil, nil }

func openDevice(info DeviceInfo) (Device, error) {
	return nil, fmt.Errorf("USB UPS access is not supported on %s yet", runtime.GOOS)
}
