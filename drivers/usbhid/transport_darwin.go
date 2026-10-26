package usbhid

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/nut-ng/nut-ng/drivers/usbhid/hid"
)

// The macOS transport uses IOKit's IOHIDDevice API, loaded at run time
// with purego so nutng needs no cgo. Devices are opened without seizing
// them, so macOS's own UPS support (the battery menu, Energy Saver
// shutdown settings) keeps working alongside nutng. UPSes need no
// permission prompt: only keyboards and similar devices are protected.

var iokit struct {
	once sync.Once
	err  error

	// CoreFoundation
	CFRelease                       func(cf uintptr)
	CFGetTypeID                     func(cf uintptr) uintptr
	CFStringGetTypeID               func() uintptr
	CFNumberGetTypeID               func() uintptr
	CFDataGetTypeID                 func() uintptr
	CFStringCreateWithCString       func(alloc uintptr, s string, encoding uint32) uintptr
	CFStringGetCString              func(s uintptr, buf *byte, size int, encoding uint32) bool
	CFNumberCreate                  func(alloc uintptr, typ int, value unsafe.Pointer) uintptr
	CFNumberGetValue                func(n uintptr, typ int, value unsafe.Pointer) bool
	CFDataGetLength                 func(d uintptr) int
	CFDataGetBytePtr                func(d uintptr) *byte
	CFDictionaryCreateMutable       func(alloc uintptr, capacity int, keyCB, valueCB uintptr) uintptr
	CFDictionarySetValue            func(d, key, value uintptr)
	CFArrayCreate                   func(alloc uintptr, values *uintptr, n int, cb uintptr) uintptr
	CFSetGetCount                   func(s uintptr) int
	CFSetGetValues                  func(s uintptr, values *uintptr)
	kCFTypeDictionaryKeyCallBacks   uintptr
	kCFTypeDictionaryValueCallBacks uintptr
	kCFTypeArrayCallBacks           uintptr

	// IOKit
	IOHIDManagerCreate                    func(alloc uintptr, options uint32) uintptr
	IOHIDManagerSetDeviceMatchingMultiple func(m, matching uintptr)
	IOHIDManagerCopyDevices               func(m uintptr) uintptr
	IOHIDDeviceGetProperty                func(d, key uintptr) uintptr
	IOHIDDeviceGetService                 func(d uintptr) uint32
	IOHIDDeviceCreate                     func(alloc uintptr, service uint32) uintptr
	IOHIDDeviceOpen                       func(d uintptr, options uint32) int32
	IOHIDDeviceClose                      func(d uintptr, options uint32) int32
	IOHIDDeviceGetReport                  func(d uintptr, typ int32, id int, report *byte, length *int) int32
	IOHIDDeviceSetReport                  func(d uintptr, typ int32, id int, report *byte, length int) int32
	IORegistryEntryGetRegistryEntryID     func(entry uint32, id *uint64) int32
	IORegistryEntryIDMatching             func(id uint64) uintptr
	IOServiceGetMatchingService           func(mainPort uint32, matching uintptr) uint32
	IOObjectRelease                       func(obj uint32) int32
}

const (
	cfStringEncodingUTF8 = 0x08000100
	cfNumberSInt32       = 3
	cfNumberSInt64       = 4

	kIOHIDReportTypeInput   = 0
	kIOHIDReportTypeFeature = 2
	kIOReturnSuccess        = 0
	kIOReturnNotPrivileged  = -536870207 // 0xe00002c1
	kIOReturnExclusive      = -536870203 // 0xe00002c5
)

func loadIOKit() error {
	iokit.once.Do(func() {
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			iokit.err = fmt.Errorf("load CoreFoundation: %w", err)
			return
		}
		io, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			iokit.err = fmt.Errorf("load IOKit: %w", err)
			return
		}
		for name, fn := range map[string]any{
			"CFRelease": &iokit.CFRelease, "CFGetTypeID": &iokit.CFGetTypeID,
			"CFStringGetTypeID": &iokit.CFStringGetTypeID, "CFNumberGetTypeID": &iokit.CFNumberGetTypeID,
			"CFDataGetTypeID": &iokit.CFDataGetTypeID, "CFStringCreateWithCString": &iokit.CFStringCreateWithCString,
			"CFStringGetCString": &iokit.CFStringGetCString, "CFNumberCreate": &iokit.CFNumberCreate,
			"CFNumberGetValue": &iokit.CFNumberGetValue, "CFDataGetLength": &iokit.CFDataGetLength,
			"CFDataGetBytePtr": &iokit.CFDataGetBytePtr, "CFDictionaryCreateMutable": &iokit.CFDictionaryCreateMutable,
			"CFDictionarySetValue": &iokit.CFDictionarySetValue, "CFArrayCreate": &iokit.CFArrayCreate,
			"CFSetGetCount": &iokit.CFSetGetCount, "CFSetGetValues": &iokit.CFSetGetValues,
		} {
			purego.RegisterLibFunc(fn, cf, name)
		}
		for name, p := range map[string]*uintptr{
			"kCFTypeDictionaryKeyCallBacks":   &iokit.kCFTypeDictionaryKeyCallBacks,
			"kCFTypeDictionaryValueCallBacks": &iokit.kCFTypeDictionaryValueCallBacks,
			"kCFTypeArrayCallBacks":           &iokit.kCFTypeArrayCallBacks,
		} {
			if *p, err = purego.Dlsym(cf, name); err != nil {
				iokit.err = fmt.Errorf("CoreFoundation %s: %w", name, err)
				return
			}
		}
		for name, fn := range map[string]any{
			"IOHIDManagerCreate": &iokit.IOHIDManagerCreate, "IOHIDManagerSetDeviceMatchingMultiple": &iokit.IOHIDManagerSetDeviceMatchingMultiple,
			"IOHIDManagerCopyDevices": &iokit.IOHIDManagerCopyDevices, "IOHIDDeviceGetProperty": &iokit.IOHIDDeviceGetProperty,
			"IOHIDDeviceGetService": &iokit.IOHIDDeviceGetService, "IOHIDDeviceCreate": &iokit.IOHIDDeviceCreate,
			"IOHIDDeviceOpen": &iokit.IOHIDDeviceOpen, "IOHIDDeviceClose": &iokit.IOHIDDeviceClose,
			"IOHIDDeviceGetReport": &iokit.IOHIDDeviceGetReport, "IOHIDDeviceSetReport": &iokit.IOHIDDeviceSetReport,
			"IORegistryEntryGetRegistryEntryID": &iokit.IORegistryEntryGetRegistryEntryID,
			"IORegistryEntryIDMatching":         &iokit.IORegistryEntryIDMatching,
			"IOServiceGetMatchingService":       &iokit.IOServiceGetMatchingService, "IOObjectRelease": &iokit.IOObjectRelease,
		} {
			purego.RegisterLibFunc(fn, io, name)
		}
	})
	return iokit.err
}

// cfString makes a CFString; the caller releases it.
func cfString(s string) uintptr {
	return iokit.CFStringCreateWithCString(0, s, cfStringEncodingUTF8)
}

// property reads a device property as a string, number or data.
func property(dev uintptr, key string) any {
	k := cfString(key)
	defer iokit.CFRelease(k)
	v := iokit.IOHIDDeviceGetProperty(dev, k) // not owned: do not release
	if v == 0 {
		return nil
	}
	switch iokit.CFGetTypeID(v) {
	case iokit.CFStringGetTypeID():
		buf := make([]byte, 512)
		if iokit.CFStringGetCString(v, &buf[0], len(buf), cfStringEncodingUTF8) {
			return strings.TrimRight(string(buf), "\x00")
		}
	case iokit.CFNumberGetTypeID():
		var n int64
		if iokit.CFNumberGetValue(v, cfNumberSInt64, unsafe.Pointer(&n)) {
			return n
		}
	case iokit.CFDataGetTypeID():
		n := iokit.CFDataGetLength(v)
		if n > 0 {
			return append([]byte(nil), unsafe.Slice(iokit.CFDataGetBytePtr(v), n)...)
		}
	}
	return nil
}

func propString(dev uintptr, key string) string { s, _ := property(dev, key).(string); return s }
func propInt(dev uintptr, key string) int64     { n, _ := property(dev, key).(int64); return n }

// matchingPages returns a CFArray of matching dictionaries for HID devices
// on the Power Device and Battery System pages.
func matchingPages() uintptr {
	key := cfString("DeviceUsagePage")
	defer iokit.CFRelease(key)
	var dicts []uintptr
	for _, page := range []int32{hid.PagePowerDevice, hid.PageBatterySystem} {
		d := iokit.CFDictionaryCreateMutable(0, 1, iokit.kCFTypeDictionaryKeyCallBacks, iokit.kCFTypeDictionaryValueCallBacks)
		n := iokit.CFNumberCreate(0, cfNumberSInt32, unsafe.Pointer(&page))
		iokit.CFDictionarySetValue(d, key, n)
		iokit.CFRelease(n)
		dicts = append(dicts, d)
	}
	arr := iokit.CFArrayCreate(0, &dicts[0], len(dicts), iokit.kCFTypeArrayCallBacks)
	for _, d := range dicts {
		iokit.CFRelease(d)
	}
	return arr
}

func enumerate() ([]DeviceInfo, error) {
	if err := loadIOKit(); err != nil {
		return nil, err
	}
	mgr := iokit.IOHIDManagerCreate(0, 0)
	if mgr == 0 {
		return nil, errors.New("IOHIDManagerCreate failed")
	}
	defer iokit.CFRelease(mgr)
	m := matchingPages()
	iokit.IOHIDManagerSetDeviceMatchingMultiple(mgr, m)
	iokit.CFRelease(m)
	set := iokit.IOHIDManagerCopyDevices(mgr)
	if set == 0 {
		return nil, nil // no matching devices
	}
	defer iokit.CFRelease(set)
	n := iokit.CFSetGetCount(set)
	if n == 0 {
		return nil, nil
	}
	devs := make([]uintptr, n)
	iokit.CFSetGetValues(set, &devs[0])

	var out []DeviceInfo
	for _, dev := range devs {
		var id uint64
		if iokit.IORegistryEntryGetRegistryEntryID(iokit.IOHIDDeviceGetService(dev), &id) != kIOReturnSuccess {
			continue
		}
		info := DeviceInfo{
			Path:         fmt.Sprintf("IOService:%#x", id),
			VendorID:     uint16(propInt(dev, "VendorID")),
			ProductID:    uint16(propInt(dev, "ProductID")),
			Manufacturer: strings.TrimSpace(propString(dev, "Manufacturer")),
			Product:      strings.TrimSpace(propString(dev, "Product")),
			Serial:       strings.TrimSpace(propString(dev, "SerialNumber")),
			Usage:        hid.NewUsage(uint16(propInt(dev, "PrimaryUsagePage")), uint16(propInt(dev, "PrimaryUsage"))),
		}
		if desc, ok := property(dev, "ReportDescriptor").([]byte); ok {
			if d, err := hid.Parse(desc); err == nil {
				info.Usage = d.Usage
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func openDevice(info DeviceInfo) (Device, error) {
	if err := loadIOKit(); err != nil {
		return nil, err
	}
	id, err := strconv.ParseUint(strings.TrimPrefix(info.Path, "IOService:"), 0, 64)
	if err != nil {
		return nil, fmt.Errorf("bad device path %q", info.Path)
	}
	svc := iokit.IOServiceGetMatchingService(0, iokit.IORegistryEntryIDMatching(id)) // consumes the dictionary
	if svc == 0 {
		return nil, fmt.Errorf("%s is gone (unplugged?)", info.Description())
	}
	dev := iokit.IOHIDDeviceCreate(0, svc)
	iokit.IOObjectRelease(svc)
	if dev == 0 {
		return nil, fmt.Errorf("IOHIDDeviceCreate failed for %s", info.Description())
	}
	switch r := iokit.IOHIDDeviceOpen(dev, 0); r {
	case kIOReturnSuccess:
	case kIOReturnNotPrivileged:
		iokit.CFRelease(dev)
		return nil, fmt.Errorf("not allowed to open %s; run nutng as root (the default service)", info.Description())
	case kIOReturnExclusive:
		iokit.CFRelease(dev)
		return nil, fmt.Errorf("%s is in use by another program that opened it exclusively (NUT's usbhid-ups?)", info.Description())
	default:
		iokit.CFRelease(dev)
		return nil, fmt.Errorf("open %s: IOKit error %#x", info.Description(), uint32(r))
	}
	desc, ok := property(dev, "ReportDescriptor").([]byte)
	if !ok {
		iokit.IOHIDDeviceClose(dev, 0)
		iokit.CFRelease(dev)
		return nil, fmt.Errorf("%s has no report descriptor", info.Description())
	}
	return newRawDevice(info, desc, &iohid{dev: dev})
}

type iohid struct{ dev uintptr }

// GetReport reads a report. buf[0] is the report ID, and IOKit returns
// numbered reports with the ID byte, as the other transports do;
// unnumbered reports start at buf[1].
func (h *iohid) GetReport(t hid.ReportType, buf []byte) (int, error) {
	typ := int32(kIOHIDReportTypeFeature)
	switch t {
	case hid.Feature:
	case hid.Input:
		typ = kIOHIDReportTypeInput
	default:
		return 0, fmt.Errorf("cannot read %s reports", t)
	}
	id := int(buf[0])
	data := buf
	if id == 0 {
		data = buf[1:]
	}
	n := len(data)
	if r := iokit.IOHIDDeviceGetReport(h.dev, typ, id, &data[0], &n); r != kIOReturnSuccess {
		return 0, fmt.Errorf("get %s report %d: IOKit error %#x", t, id, uint32(r))
	}
	if id == 0 {
		n++
	}
	return n, nil
}

func (h *iohid) SetFeature(buf []byte) error {
	id := int(buf[0])
	data := buf
	if id == 0 {
		data = buf[1:]
	}
	if r := iokit.IOHIDDeviceSetReport(h.dev, kIOHIDReportTypeFeature, id, &data[0], len(data)); r != kIOReturnSuccess {
		return fmt.Errorf("set feature report %d: IOKit error %#x", id, uint32(r))
	}
	return nil
}

func (h *iohid) Close() error {
	iokit.IOHIDDeviceClose(h.dev, 0)
	iokit.CFRelease(h.dev)
	return nil
}
