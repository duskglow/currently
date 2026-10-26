package usbhid

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/nut-ng/nut-ng/drivers/usbhid/hid"
)

// The Linux transport uses hidraw: /dev/hidrawN for reports, and sysfs for
// the report descriptor and USB strings. The kernel's generic HID driver
// binds UPSes, so no driver detaching (as with libusb) is needed.

const sysHidraw = "/sys/class/hidraw"

func enumerate() ([]DeviceInfo, error) {
	entries, err := os.ReadDir(sysHidraw)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // no HID devices, or hidraw not built into the kernel
	}
	if err != nil {
		return nil, err
	}
	var out []DeviceInfo
	for _, e := range entries {
		if info, err := describeHidraw(e.Name()); err == nil {
			out = append(out, info)
		}
	}
	return out, nil
}

func describeHidraw(name string) (DeviceInfo, error) {
	sys := filepath.Join(sysHidraw, name, "device")
	info := DeviceInfo{Path: "/dev/" + name}

	uevent, err := os.ReadFile(filepath.Join(sys, "uevent"))
	if err != nil {
		return info, err
	}
	for _, line := range strings.Split(string(uevent), "\n") {
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "HID_ID": // bus:vendor:product, e.g. 0003:00000764:00000601
			parts := strings.Split(v, ":")
			if len(parts) == 3 {
				vid, _ := strconv.ParseUint(parts[1], 16, 32)
				pid, _ := strconv.ParseUint(parts[2], 16, 32)
				info.VendorID, info.ProductID = uint16(vid), uint16(pid)
			}
		case "HID_NAME":
			info.Product = v
		case "HID_UNIQ":
			info.Serial = v
		}
	}
	// The USB device, two levels up (interface, then device), has the
	// separate manufacturer and product strings.
	if real, err := filepath.EvalSymlinks(sys); err == nil {
		usb := filepath.Dir(filepath.Dir(real))
		if m := readTrim(filepath.Join(usb, "manufacturer")); m != "" {
			info.Manufacturer = m
		}
		if p := readTrim(filepath.Join(usb, "product")); p != "" {
			info.Product = p
		}
		if s := readTrim(filepath.Join(usb, "serial")); s != "" {
			info.Serial = s
		}
	}
	if desc, err := os.ReadFile(filepath.Join(sys, "report_descriptor")); err == nil {
		if d, err := hid.Parse(desc); err == nil {
			info.Usage = d.Usage
		}
	}
	return info, nil
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func openDevice(info DeviceInfo) (Device, error) {
	name := filepath.Base(info.Path)
	desc, err := os.ReadFile(filepath.Join(sysHidraw, name, "device", "report_descriptor"))
	if err != nil {
		return nil, fmt.Errorf("read report descriptor: %w", err)
	}
	f, err := os.OpenFile(info.Path, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("permission denied opening %s (%s). Install the nutng udev rule "+
				"(packaging/linux/90-nutng-ups.rules) and replug the UPS, or run nutng as root", info.Path, info.Description())
		}
		return nil, err
	}
	return newRawDevice(info, desc, &hidraw{f: f})
}

type hidraw struct{ f *os.File }

// ioctl request numbers from linux/hidraw.h: _IOC(_IOC_READ|_IOC_WRITE,
// 'H', nr, len).
func hidiocRW(nr, length uintptr) uintptr { return 3<<30 | length<<16 | 'H'<<8 | nr }

const (
	hidiocSFeature = 0x06
	hidiocGFeature = 0x07
	hidiocGInput   = 0x0A // Linux 5.11 and later
)

func (h *hidraw) ioctl(nr uintptr, buf []byte) (int, error) {
	conn, err := h.f.SyscallConn()
	if err != nil {
		return 0, err
	}
	var n uintptr
	var errno unix.Errno
	err = conn.Control(func(fd uintptr) {
		n, _, errno = unix.Syscall(unix.SYS_IOCTL, fd, hidiocRW(nr, uintptr(len(buf))), uintptr(unsafe.Pointer(&buf[0])))
	})
	if err != nil {
		return 0, err
	}
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

func (h *hidraw) GetReport(t hid.ReportType, buf []byte) (int, error) {
	switch t {
	case hid.Feature:
		return h.ioctl(hidiocGFeature, buf)
	case hid.Input:
		return h.ioctl(hidiocGInput, buf)
	}
	return 0, fmt.Errorf("cannot read %s reports", t)
}

func (h *hidraw) SetFeature(buf []byte) error {
	_, err := h.ioctl(hidiocSFeature, buf)
	return err
}

func (h *hidraw) Close() error { return h.f.Close() }
