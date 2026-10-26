package usbhid

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/duskglow/currently/drivers/usbhid/hid"
)

// The Windows transport uses the HID class driver through hid.dll, so no
// driver replacement (libusb, WinUSB, Zadig) is needed. Windows does not
// expose the raw report descriptor; fields come from the HidP_* "caps"
// functions and values are decoded by HidP_GetUsageValue/HidP_GetUsages.

var (
	hidDLL                    = windows.NewLazySystemDLL("hid.dll")
	procHidDGetAttributes     = hidDLL.NewProc("HidD_GetAttributes")
	procHidDGetPreparsedData  = hidDLL.NewProc("HidD_GetPreparsedData")
	procHidDFreePreparsedData = hidDLL.NewProc("HidD_FreePreparsedData")
	procHidDGetFeature        = hidDLL.NewProc("HidD_GetFeature")
	procHidDSetFeature        = hidDLL.NewProc("HidD_SetFeature")
	procHidDGetInputReport    = hidDLL.NewProc("HidD_GetInputReport")
	procHidDGetManufacturer   = hidDLL.NewProc("HidD_GetManufacturerString")
	procHidDGetProduct        = hidDLL.NewProc("HidD_GetProductString")
	procHidDGetSerial         = hidDLL.NewProc("HidD_GetSerialNumberString")
	procHidDGetIndexedString  = hidDLL.NewProc("HidD_GetIndexedString")
	procHidPGetCaps           = hidDLL.NewProc("HidP_GetCaps")
	procHidPGetValueCaps      = hidDLL.NewProc("HidP_GetValueCaps")
	procHidPGetButtonCaps     = hidDLL.NewProc("HidP_GetButtonCaps")
	procHidPGetLinkCollNodes  = hidDLL.NewProc("HidP_GetLinkCollectionNodes")
	procHidPGetUsageValue     = hidDLL.NewProc("HidP_GetUsageValue")
	procHidPSetUsageValue     = hidDLL.NewProc("HidP_SetUsageValue")
	procHidPGetUsages         = hidDLL.NewProc("HidP_GetUsages")
	guidDevinterfaceHID       = windows.GUID{Data1: 0x4D1E55B2, Data2: 0xF16F, Data3: 0x11CF, Data4: [8]byte{0x88, 0xCB, 0x00, 0x11, 0x11, 0x00, 0x00, 0x30}}
	errHIDPStatus             = errors.New("HID parser error")
)

const hidpStatusSuccess = 0x00110000

// HIDP_REPORT_TYPE values.
const (
	hidpInput   = 0
	hidpOutput  = 1
	hidpFeature = 2
)

type hiddAttributes struct {
	Size          uint32
	VendorID      uint16
	ProductID     uint16
	VersionNumber uint16
	_             uint16
}

type hidpCaps struct {
	Usage                     uint16
	UsagePage                 uint16
	InputReportByteLength     uint16
	OutputReportByteLength    uint16
	FeatureReportByteLength   uint16
	Reserved                  [17]uint16
	NumberLinkCollectionNodes uint16
	NumberInputButtonCaps     uint16
	NumberInputValueCaps      uint16
	NumberInputDataIndices    uint16
	NumberOutputButtonCaps    uint16
	NumberOutputValueCaps     uint16
	NumberOutputDataIndices   uint16
	NumberFeatureButtonCaps   uint16
	NumberFeatureValueCaps    uint16
	NumberFeatureDataIndices  uint16
}

// hidpValueCaps mirrors HIDP_VALUE_CAPS (72 bytes).
type hidpValueCaps struct {
	UsagePage         uint16
	ReportID          uint8
	IsAlias           uint8
	BitField          uint16
	LinkCollection    uint16
	LinkUsage         uint16
	LinkUsagePage     uint16
	IsRange           uint8
	IsStringRange     uint8
	IsDesignatorRange uint8
	IsAbsolute        uint8
	HasNull           uint8
	Reserved          uint8
	BitSize           uint16
	ReportCount       uint16
	Reserved2         [5]uint16
	UnitsExp          uint32
	Units             uint32
	LogicalMin        int32
	LogicalMax        int32
	PhysicalMin       int32
	PhysicalMax       int32
	// Range: UsageMin, UsageMax; NotRange: Usage, Reserved.
	UsageMin uint16
	UsageMax uint16
	_        [12]byte
}

// hidpButtonCaps mirrors HIDP_BUTTON_CAPS (72 bytes).
type hidpButtonCaps struct {
	UsagePage         uint16
	ReportID          uint8
	IsAlias           uint8
	BitField          uint16
	LinkCollection    uint16
	LinkUsage         uint16
	LinkUsagePage     uint16
	IsRange           uint8
	IsStringRange     uint8
	IsDesignatorRange uint8
	IsAbsolute        uint8
	ReportCount       uint16
	Reserved2         uint16
	Reserved          [9]uint32
	UsageMin          uint16
	UsageMax          uint16
	_                 [12]byte
}

// hidpLinkCollectionNode mirrors HIDP_LINK_COLLECTION_NODE.
type hidpLinkCollectionNode struct {
	LinkUsage        uint16
	LinkUsagePage    uint16
	Parent           uint16
	NumberOfChildren uint16
	NextSibling      uint16
	FirstChild       uint16
	Bits             uint32 // CollectionType:8, IsAlias:1
	UserContext      uintptr
}

func init() {
	if unsafe.Sizeof(hidpValueCaps{}) != 72 || unsafe.Sizeof(hidpButtonCaps{}) != 72 || unsafe.Sizeof(hidpCaps{}) != 64 {
		panic("usbhid: HIDP struct layout mismatch")
	}
}

func boolCall(p *windows.LazyProc, args ...uintptr) error {
	r, _, err := p.Call(args...)
	if r == 0 {
		if err == nil || err == windows.ERROR_SUCCESS {
			return fmt.Errorf("%s failed", p.Name)
		}
		return fmt.Errorf("%s: %w", p.Name, err)
	}
	return nil
}

func statusCall(p *windows.LazyProc, args ...uintptr) error {
	r, _, _ := p.Call(args...)
	if uint32(r) != hidpStatusSuccess {
		return fmt.Errorf("%s: %w (status 0x%08x)", p.Name, errHIDPStatus, uint32(r))
	}
	return nil
}

func openHandle(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, 0, 0)
}

func hidString(p *windows.LazyProc, h windows.Handle, extra ...uintptr) string {
	buf := make([]uint16, 256)
	args := []uintptr{uintptr(h)}
	args = append(args, extra...)
	args = append(args, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2))
	if boolCall(p, args...) != nil {
		return ""
	}
	return windows.UTF16ToString(buf)
}

// enumerate lists HID devices. Opening with no access rights works even for
// devices the system holds open, and is enough to read attributes.
func enumerate() ([]DeviceInfo, error) {
	paths, err := windows.CM_Get_Device_Interface_List("", &guidDevinterfaceHID, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil {
		return nil, fmt.Errorf("list HID devices: %w", err)
	}
	var out []DeviceInfo
	for _, path := range paths {
		if path == "" {
			continue
		}
		h, err := openHandle(path, 0)
		if err != nil {
			continue
		}
		info, err := describe(h, path)
		_ = windows.CloseHandle(h)
		if err == nil {
			out = append(out, info)
		}
	}
	return out, nil
}

func describe(h windows.Handle, path string) (DeviceInfo, error) {
	attr := hiddAttributes{Size: uint32(unsafe.Sizeof(hiddAttributes{}))}
	if err := boolCall(procHidDGetAttributes, uintptr(h), uintptr(unsafe.Pointer(&attr))); err != nil {
		return DeviceInfo{}, err
	}
	info := DeviceInfo{
		Path:         path,
		VendorID:     attr.VendorID,
		ProductID:    attr.ProductID,
		Manufacturer: hidString(procHidDGetManufacturer, h),
		Product:      hidString(procHidDGetProduct, h),
		Serial:       hidString(procHidDGetSerial, h),
	}
	var pp uintptr
	if err := boolCall(procHidDGetPreparsedData, uintptr(h), uintptr(unsafe.Pointer(&pp))); err == nil {
		var caps hidpCaps
		if statusCall(procHidPGetCaps, pp, uintptr(unsafe.Pointer(&caps))) == nil {
			info.Usage = hid.NewUsage(caps.UsagePage, caps.Usage)
		}
		_, _, _ = procHidDFreePreparsedData.Call(pp)
	}
	return info, nil
}

// winField is a field plus how to read it.
type winField struct {
	hid.Field
	button bool // read with HidP_GetUsages
}

type winDevice struct {
	mu     sync.Mutex
	h      windows.Handle
	pp     uintptr
	caps   hidpCaps
	info   DeviceInfo
	fields []winField
	byPath map[string]*winField
	pub    []hid.Field
}

func openDevice(info DeviceInfo) (Device, error) {
	h, err := openHandle(info.Path, windows.GENERIC_READ|windows.GENERIC_WRITE)
	if err != nil {
		// Read-only still allows reading reports on most devices.
		if h, err = openHandle(info.Path, windows.GENERIC_READ); err != nil {
			return nil, fmt.Errorf("open %s: %w", info.Description(), err)
		}
	}
	d := &winDevice{h: h, info: info}
	if err := d.load(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func (d *winDevice) load() error {
	if err := boolCall(procHidDGetPreparsedData, uintptr(d.h), uintptr(unsafe.Pointer(&d.pp))); err != nil {
		return err
	}
	if err := statusCall(procHidPGetCaps, d.pp, uintptr(unsafe.Pointer(&d.caps))); err != nil {
		return err
	}
	names := d.collectionNames()
	pathOf := func(link uint16, u hid.Usage) string {
		if n, ok := names[link]; ok {
			return n + "." + u.Name()
		}
		return u.Name()
	}

	for _, rt := range []struct {
		t       hid.ReportType
		hidp    uintptr
		values  uint16
		buttons uint16
	}{
		{hid.Input, hidpInput, d.caps.NumberInputValueCaps, d.caps.NumberInputButtonCaps},
		{hid.Feature, hidpFeature, d.caps.NumberFeatureValueCaps, d.caps.NumberFeatureButtonCaps},
	} {
		if n := rt.values; n > 0 {
			vc := make([]hidpValueCaps, n)
			if err := statusCall(procHidPGetValueCaps, rt.hidp, uintptr(unsafe.Pointer(&vc[0])), uintptr(unsafe.Pointer(&n)), d.pp); err != nil {
				return err
			}
			for _, c := range vc[:n] {
				if c.IsRange == 0 && c.ReportCount > 1 {
					continue // arrays of values are not used by UPSes
				}
				lo, hi := c.UsageMin, c.UsageMin
				if c.IsRange != 0 {
					hi = c.UsageMax
				}
				for u := uint32(lo); u <= uint32(hi); u++ {
					us := hid.NewUsage(c.UsagePage, uint16(u))
					d.fields = append(d.fields, winField{Field: hid.Field{
						Path: pathOf(c.LinkCollection, us), Usage: us, Type: rt.t, ReportID: c.ReportID,
						BitOffset: -1, BitSize: int(c.BitSize),
						LogicalMin: c.LogicalMin, LogicalMax: c.LogicalMax,
						PhysicalMin: c.PhysicalMin, PhysicalMax: c.PhysicalMax,
						Unit: c.Units, UnitExponent: nibble(c.UnitsExp),
						Flag: c.BitSize == 1, Collection: c.LinkCollection,
					}})
				}
			}
		}
		if n := rt.buttons; n > 0 {
			bc := make([]hidpButtonCaps, n)
			if err := statusCall(procHidPGetButtonCaps, rt.hidp, uintptr(unsafe.Pointer(&bc[0])), uintptr(unsafe.Pointer(&n)), d.pp); err != nil {
				return err
			}
			for _, c := range bc[:n] {
				lo, hi := c.UsageMin, c.UsageMin
				if c.IsRange != 0 {
					hi = c.UsageMax
				}
				for u := uint32(lo); u <= uint32(hi); u++ {
					us := hid.NewUsage(c.UsagePage, uint16(u))
					d.fields = append(d.fields, winField{button: true, Field: hid.Field{
						Path: pathOf(c.LinkCollection, us), Usage: us, Type: rt.t, ReportID: c.ReportID,
						BitOffset: -1, BitSize: 1, LogicalMin: 0, LogicalMax: 1,
						Flag: true, Collection: c.LinkCollection,
					}})
				}
			}
		}
	}

	d.byPath = map[string]*winField{}
	for i := range d.fields {
		f := &d.fields[i]
		if old, ok := d.byPath[f.Path]; !ok || (old.Type != hid.Feature && f.Type == hid.Feature) {
			d.byPath[f.Path] = f
		}
		d.pub = append(d.pub, f.Field)
	}
	return nil
}

// collectionNames returns the path of every link collection, numbering
// siblings that share a usage as the descriptor parser does.
func (d *winDevice) collectionNames() map[uint16]string {
	n := uint32(d.caps.NumberLinkCollectionNodes)
	if n == 0 {
		return nil
	}
	nodes := make([]hidpLinkCollectionNode, n)
	if statusCall(procHidPGetLinkCollNodes, uintptr(unsafe.Pointer(&nodes[0])), uintptr(unsafe.Pointer(&n)), d.pp) != nil {
		return nil
	}
	nodes = nodes[:n]
	children := map[uint16][]uint16{}
	for i := 1; i < len(nodes); i++ {
		p := nodes[i].Parent
		children[p] = append(children[p], uint16(i))
	}
	elem := map[uint16]string{0: hid.NewUsage(nodes[0].LinkUsagePage, nodes[0].LinkUsage).Name()}
	for _, kids := range children {
		sort.Slice(kids, func(i, j int) bool { return kids[i] < kids[j] })
		counts := map[hid.Usage]int{}
		for _, k := range kids {
			counts[hid.NewUsage(nodes[k].LinkUsagePage, nodes[k].LinkUsage)]++
		}
		seen := map[hid.Usage]int{}
		for _, k := range kids {
			u := hid.NewUsage(nodes[k].LinkUsagePage, nodes[k].LinkUsage)
			name := u.Name()
			if counts[u] > 1 {
				seen[u]++
				name += "[" + strconv.Itoa(seen[u]) + "]"
			}
			elem[k] = name
		}
	}
	names := map[uint16]string{}
	var full func(i uint16, depth int) string
	full = func(i uint16, depth int) string {
		if s, ok := names[i]; ok {
			return s
		}
		s := elem[i]
		if i != 0 && depth < 64 {
			s = full(nodes[i].Parent, depth+1) + "." + s
		}
		names[i] = s
		return s
	}
	for i := range nodes {
		full(uint16(i), 0)
	}
	return names
}

func nibble(v uint32) int8 {
	if v > 7 && v <= 0xF {
		return int8(v) - 16
	}
	return int8(v)
}

func (d *winDevice) Info() DeviceInfo    { return d.info }
func (d *winDevice) Fields() []hid.Field { return d.pub }

func (d *winDevice) Read(ctx context.Context) (map[string]int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	values := map[string]int64{}
	ok, failed := 0, 0
	var lastErr error
	for _, rt := range []struct {
		t    hid.ReportType
		hidp uintptr
		get  *windows.LazyProc
		size uint16
	}{
		{hid.Feature, hidpFeature, procHidDGetFeature, d.caps.FeatureReportByteLength},
		{hid.Input, hidpInput, procHidDGetInputReport, d.caps.InputReportByteLength},
	} {
		if rt.size == 0 {
			continue
		}
		for _, id := range d.reportIDs(rt.t) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			buf := make([]byte, rt.size)
			buf[0] = id
			if err := boolCall(rt.get, uintptr(d.h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); err != nil {
				failed++
				lastErr = err
				continue
			}
			ok++
			d.decode(rt.t, rt.hidp, id, buf, values)
		}
	}
	if ok == 0 && failed > 0 {
		return nil, fmt.Errorf("could not read any report from %s: %w", d.info.Description(), lastErr)
	}
	return values, nil
}

func (d *winDevice) reportIDs(t hid.ReportType) []uint8 {
	seen := map[uint8]bool{}
	var ids []uint8
	for _, f := range d.fields {
		if f.Type == t && !seen[f.ReportID] {
			seen[f.ReportID] = true
			ids = append(ids, f.ReportID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (d *winDevice) decode(t hid.ReportType, hidp uintptr, id uint8, buf []byte, values map[string]int64) {
	type group struct {
		page uint16
		link uint16
	}
	on := map[group]map[uint16]bool{}
	for i := range d.fields {
		f := &d.fields[i]
		if f.Type != t || f.ReportID != id {
			continue
		}
		if f.button {
			g := group{f.Usage.Page(), f.Collection}
			set, done := on[g]
			if !done {
				set = d.usagesOn(hidp, g.page, g.link, buf)
				on[g] = set
			}
			if set == nil {
				continue
			}
			v := int64(0)
			if set[f.Usage.ID()] {
				v = 1
			}
			values[f.Path] = v
			continue
		}
		var raw uint32
		if statusCall(procHidPGetUsageValue, hidp, uintptr(f.Usage.Page()), uintptr(f.Collection), uintptr(f.Usage.ID()),
			uintptr(unsafe.Pointer(&raw)), d.pp, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))) == nil {
			values[f.Path] = f.SignExtend(raw)
		}
	}
}

func (d *winDevice) usagesOn(hidp uintptr, page, link uint16, buf []byte) map[uint16]bool {
	list := make([]uint16, 256)
	n := uint32(len(list))
	if statusCall(procHidPGetUsages, hidp, uintptr(page), uintptr(link), uintptr(unsafe.Pointer(&list[0])),
		uintptr(unsafe.Pointer(&n)), d.pp, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))) != nil {
		return nil
	}
	set := map[uint16]bool{}
	for _, u := range list[:n] {
		set[u] = true
	}
	return set
}

func (d *winDevice) Write(path string, value int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.byPath[path]
	if !ok || f.Type != hid.Feature || f.button {
		return fmt.Errorf("%s is not a writable feature value", path)
	}
	buf := make([]byte, d.caps.FeatureReportByteLength)
	buf[0] = f.ReportID
	if err := boolCall(procHidDGetFeature, uintptr(d.h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); err != nil {
		return err
	}
	if err := statusCall(procHidPSetUsageValue, hidpFeature, uintptr(f.Usage.Page()), uintptr(f.Collection), uintptr(f.Usage.ID()),
		uintptr(uint32(value)), d.pp, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); err != nil {
		return err
	}
	return boolCall(procHidDSetFeature, uintptr(d.h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
}

func (d *winDevice) String(index int) (string, error) {
	if index <= 0 {
		return "", fmt.Errorf("invalid string index %d", index)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	s := hidString(procHidDGetIndexedString, d.h, uintptr(index))
	if s == "" {
		return "", fmt.Errorf("string %d not available", index)
	}
	return s, nil
}

func (d *winDevice) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pp != 0 {
		_, _, _ = procHidDFreePreparsedData.Call(d.pp)
		d.pp = 0
	}
	if d.h != 0 {
		err := windows.CloseHandle(d.h)
		d.h = 0
		return err
	}
	return nil
}
