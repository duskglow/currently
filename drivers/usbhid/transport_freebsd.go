package usbhid

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/duskglow/currently/drivers/usbhid/hid"
)

// The FreeBSD transport opens /dev/uhidN (uhid(4), the default HID driver
// through FreeBSD 14) or /dev/hidrawN (hidraw(4), used when
// hw.usb.usbhid.enable=1). Both take the same report ioctls, which use
// struct usb_gen_descriptor (struct hidraw_gen_descriptor is the same
// layout). Device identity comes from the newbus sysctls (dev.uhid.N.*).

// genDescriptor mirrors struct usb_gen_descriptor.
type genDescriptor struct {
	data        unsafe.Pointer
	langID      uint16
	maxLen      uint16
	actLen      uint16
	offset      uint16
	configIndex uint8
	stringIndex uint8
	ifaceIndex  uint8
	altifIndex  uint8
	endptIndex  uint8
	reportType  uint8
	reserved    [8]uint8
}

// ioctl numbers from dev/usb/usb_ioctl.h and dev/hid/hidraw.h.
const (
	iocOut   = 0x40000000
	iocIn    = 0x80000000
	iocInOut = iocIn | iocOut
)

func iocU(dir, nr uintptr) uintptr {
	return dir | (unsafe.Sizeof(genDescriptor{})&0x1fff)<<16 | 'U'<<8 | nr
}

var (
	usbGetReportDesc = iocU(iocInOut, 21)
	usbGetReport     = iocU(iocInOut, 23)
	usbSetReport     = iocU(iocIn, 24)
)

// UHID_*_REPORT / HIDRAW_*_REPORT.
const (
	bsdInputReport   = 1
	bsdFeatureReport = 3
)

// upsVendors are USB vendors whose HID devices are UPSes, for when the
// report descriptor cannot be read without opening the device.
var upsVendors = map[uint16]bool{0x051d: true, 0x0764: true, 0x0501: true, 0x0463: true,
	0x09ae: true, 0x10af: true, 0x050d: true, 0x0d9f: true}

func enumerate() ([]DeviceInfo, error) {
	var out []DeviceInfo
	for _, pattern := range []string{"/dev/hidraw*", "/dev/uhid*"} {
		paths, _ := filepath.Glob(pattern)
		for _, p := range paths {
			if info, ok := describeBSD(p); ok {
				out = append(out, info)
			}
		}
	}
	return out, nil
}

// describeBSD reads a device's identity from its sysctls, and its usage
// from the report descriptor when the device can be opened.
func describeBSD(path string) (DeviceInfo, bool) {
	name := filepath.Base(path) // uhid0, hidraw1
	unit := strings.TrimLeft(name, "abcdefghijklmnopqrstuvwxyz")
	driver := strings.TrimSuffix(name, unit)
	if unit == "" {
		return DeviceInfo{}, false
	}
	info := DeviceInfo{Path: path}
	// Walk up the newbus tree (hidraw0 → hidbus0 → usbhid0) to the node
	// with USB pnpinfo.
	node := "dev." + driver + "." + unit
	for range 4 {
		pnp, _ := unix.Sysctl(node + ".%pnpinfo")
		kv := parsePnP(pnp)
		if v, ok := kv["vendor"]; ok && info.VendorID == 0 {
			id, _ := strconv.ParseUint(strings.TrimPrefix(v, "0x"), 16, 16)
			info.VendorID = uint16(id)
			id, _ = strconv.ParseUint(strings.TrimPrefix(kv["product"], "0x"), 16, 16)
			info.ProductID = uint16(id)
		}
		if s := kv["sernum"]; s != "" && info.Serial == "" {
			info.Serial = s
		}
		if p, ok := kv["page"]; ok && info.Usage == 0 {
			page, _ := strconv.ParseUint(strings.TrimPrefix(p, "0x"), 16, 16)
			usage, _ := strconv.ParseUint(strings.TrimPrefix(kv["usage"], "0x"), 16, 16)
			info.Usage = hid.NewUsage(uint16(page), uint16(usage))
		}
		if desc, _ := unix.Sysctl(node + ".%desc"); desc != "" && info.Product == "" {
			// "CPS CP1500PFCLCD, class 0/0, rev 1.10/0.01, addr 2"
			d, _, _ := strings.Cut(desc, ", class")
			info.Product = strings.TrimSpace(d)
		}
		parent, err := unix.Sysctl(node + ".%parent")
		if err != nil || parent == "" {
			break
		}
		pu := strings.TrimLeft(parent, "abcdefghijklmnopqrstuvwxyz")
		node = "dev." + strings.TrimSuffix(parent, pu) + "." + pu
	}
	if desc, err := readDescriptorAt(path); err == nil {
		if d, err := hid.Parse(desc); err == nil {
			info.Usage = d.Usage
		}
	} else if info.Usage == 0 && upsVendors[info.VendorID] {
		// Busy (uhid allows one opener) or not permitted: trust the vendor.
		info.Usage = hid.NewUsage(hid.PagePowerDevice, 0x04)
	}
	return info, true
}

// parsePnP splits "vendor=0x0764 product=0x0601 sernum=\"X\"" into keys.
func parsePnP(s string) map[string]string {
	out := map[string]string{}
	for _, f := range strings.Fields(s) {
		if k, v, ok := strings.Cut(f, "="); ok {
			out[k] = strings.Trim(v, `"`)
		}
	}
	return out
}

func readDescriptorAt(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return (&bsdHID{f: f}).descriptor()
}

func openDevice(info DeviceInfo) (Device, error) {
	f, err := os.OpenFile(info.Path, os.O_RDWR, 0)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrPermission):
			return nil, fmt.Errorf("permission denied opening %s (%s); run Currently as root (the default service)", info.Path, info.Description())
		case errors.Is(err, unix.EBUSY):
			return nil, fmt.Errorf("%s (%s) is in use by another program (NUT's usbhid-ups, or another Currently?)", info.Path, info.Description())
		}
		return nil, err
	}
	h := &bsdHID{f: f}
	desc, err := h.descriptor()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("read report descriptor of %s: %w", info.Path, err)
	}
	return newRawDevice(info, desc, h)
}

type bsdHID struct{ f *os.File }

func (h *bsdHID) ioctl(req uintptr, gd *genDescriptor) error {
	conn, err := h.f.SyscallConn()
	if err != nil {
		return err
	}
	var errno unix.Errno
	err = conn.Control(func(fd uintptr) {
		_, _, errno = unix.Syscall(unix.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(gd)))
	})
	if err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

func (h *bsdHID) descriptor() ([]byte, error) {
	buf := make([]byte, 4096)
	gd := genDescriptor{data: unsafe.Pointer(&buf[0]), maxLen: uint16(len(buf))}
	err := h.ioctl(usbGetReportDesc, &gd)
	runtime.KeepAlive(buf)
	if err != nil {
		return nil, err
	}
	if gd.actLen == 0 || int(gd.actLen) > len(buf) {
		return nil, errors.New("empty report descriptor")
	}
	return buf[:gd.actLen], nil
}

// report runs a get or set report ioctl. buf[0] is the report ID; for
// unnumbered reports (ID 0) the device data has no ID byte, so it starts
// at buf[1].
func (h *bsdHID) report(req uintptr, typ uint8, buf []byte) error {
	data := buf
	if buf[0] == 0 {
		data = buf[1:]
	}
	if len(data) == 0 {
		return errors.New("empty report")
	}
	gd := genDescriptor{data: unsafe.Pointer(&data[0]), maxLen: uint16(len(data)), reportType: typ}
	err := h.ioctl(req, &gd)
	runtime.KeepAlive(data)
	return err
}

func (h *bsdHID) GetReport(t hid.ReportType, buf []byte) (int, error) {
	typ := uint8(bsdFeatureReport)
	switch t {
	case hid.Feature:
	case hid.Input:
		typ = bsdInputReport
	default:
		return 0, fmt.Errorf("cannot read %s reports", t)
	}
	if err := h.report(usbGetReport, typ, buf); err != nil {
		return 0, err
	}
	return len(buf), nil
}

func (h *bsdHID) SetFeature(buf []byte) error { return h.report(usbSetReport, bsdFeatureReport, buf) }

func (h *bsdHID) Close() error { return h.f.Close() }
