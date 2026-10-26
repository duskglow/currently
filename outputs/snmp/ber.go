package snmp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// BER (the subset of ASN.1 Basic Encoding Rules that SNMP uses).

// ASN.1 and SNMP tags.
const (
	tagInteger     = 0x02
	tagOctetString = 0x04
	tagNull        = 0x05
	tagOID         = 0x06
	tagSequence    = 0x30
	tagIPAddress   = 0x40
	tagCounter32   = 0x41
	tagGauge32     = 0x42
	tagTimeTicks   = 0x43
	tagCounter64   = 0x46

	tagNoSuchObject   = 0x80
	tagNoSuchInstance = 0x81
	tagEndOfMibView   = 0x82

	pduGet      = 0xA0
	pduGetNext  = 0xA1
	pduResponse = 0xA2
	pduSet      = 0xA3
	pduTrapV1   = 0xA4
	pduGetBulk  = 0xA5
	pduInform   = 0xA6
	pduTrapV2   = 0xA7
	pduReport   = 0xA8
)

var errMalformed = errors.New("malformed SNMP message")

// OID is an object identifier.
type OID []uint32

// ParseOID parses dotted form such as "1.3.6.1.2.1.33".
func ParseOID(s string) (OID, error) {
	s = strings.TrimPrefix(s, ".")
	parts := strings.Split(s, ".")
	o := make(OID, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("bad OID %q", s)
		}
		o[i] = uint32(n)
	}
	return o, nil
}

func mustOID(s string) OID {
	o, err := ParseOID(s)
	if err != nil {
		panic(err)
	}
	return o
}

func (o OID) String() string {
	parts := make([]string, len(o))
	for i, n := range o {
		parts[i] = strconv.FormatUint(uint64(n), 10)
	}
	return strings.Join(parts, ".")
}

// Append returns a new OID with arcs added.
func (o OID) Append(arcs ...uint32) OID {
	out := make(OID, 0, len(o)+len(arcs))
	return append(append(out, o...), arcs...)
}

// Compare orders OIDs lexicographically, as SNMP walks them.
func (o OID) Compare(p OID) int {
	for i := 0; i < len(o) && i < len(p); i++ {
		switch {
		case o[i] < p[i]:
			return -1
		case o[i] > p[i]:
			return 1
		}
	}
	switch {
	case len(o) < len(p):
		return -1
	case len(o) > len(p):
		return 1
	}
	return 0
}

// HasPrefix reports whether p is a prefix of o.
func (o OID) HasPrefix(p OID) bool {
	return len(o) >= len(p) && o[:len(p)].Compare(p) == 0
}

// Value is an SNMP variable value.
type Value struct {
	Tag   byte
	Int   int64  // Integer, Counter32, Gauge32, TimeTicks, Counter64
	Bytes []byte // OctetString, IPAddress
	OID   OID
}

// Constructors for the value types used here.
func intValue(v int64) Value     { return Value{Tag: tagInteger, Int: v} }
func gaugeValue(v int64) Value   { return Value{Tag: tagGauge32, Int: v} }
func counterValue(v int64) Value { return Value{Tag: tagCounter32, Int: v} }
func ticksValue(v int64) Value   { return Value{Tag: tagTimeTicks, Int: v} }
func stringValue(s string) Value { return Value{Tag: tagOctetString, Bytes: []byte(s)} }
func bytesValue(b []byte) Value  { return Value{Tag: tagOctetString, Bytes: b} }
func oidValue(o OID) Value       { return Value{Tag: tagOID, OID: o} }
func nullValue() Value           { return Value{Tag: tagNull} }

// VarBind is a name and value.
type VarBind struct {
	Name  OID
	Value Value
}

// --- encoding ---

func appendLength(b []byte, n int) []byte {
	if n < 0x80 {
		return append(b, byte(n))
	}
	var tmp [8]byte
	i := len(tmp)
	for n > 0 {
		i--
		tmp[i] = byte(n)
		n >>= 8
	}
	b = append(b, 0x80|byte(len(tmp)-i))
	return append(b, tmp[i:]...)
}

func appendTLV(b []byte, tag byte, content []byte) []byte {
	b = append(b, tag)
	b = appendLength(b, len(content))
	return append(b, content...)
}

func encodeInt(v int64) []byte {
	n := 1
	for x := v; x > 127 || x < -128; x >>= 8 {
		n++
	}
	out := make([]byte, n)
	for i := n - 1; i >= 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	return out
}

func encodeUint(v uint64) []byte {
	var tmp [9]byte
	i := len(tmp)
	for {
		i--
		tmp[i] = byte(v)
		v >>= 8
		if v == 0 {
			break
		}
	}
	if tmp[i]&0x80 != 0 {
		i--
		tmp[i] = 0
	}
	return append([]byte(nil), tmp[i:]...)
}

func encodeOID(o OID) []byte {
	for len(o) < 2 {
		o = append(o[:len(o):len(o)], 0)
	}
	out := appendBase128(nil, o[0]*40+o[1])
	for _, n := range o[2:] {
		out = appendBase128(out, n)
	}
	return out
}

func appendBase128(b []byte, n uint32) []byte {
	var tmp [5]byte
	i := len(tmp) - 1
	tmp[i] = byte(n & 0x7f)
	for n >>= 7; n > 0; n >>= 7 {
		i--
		tmp[i] = byte(n&0x7f) | 0x80
	}
	return append(b, tmp[i:]...)
}

func appendInt(b []byte, v int64) []byte { return appendTLV(b, tagInteger, encodeInt(v)) }

func appendOctets(b []byte, s []byte) []byte { return appendTLV(b, tagOctetString, s) }

func appendValue(b []byte, v Value) []byte {
	switch v.Tag {
	case tagInteger:
		return appendInt(b, v.Int)
	case tagCounter32, tagGauge32, tagTimeTicks:
		return appendTLV(b, v.Tag, encodeUint(uint64(uint32(v.Int))))
	case tagCounter64:
		return appendTLV(b, v.Tag, encodeUint(uint64(v.Int)))
	case tagOctetString, tagIPAddress:
		return appendTLV(b, v.Tag, v.Bytes)
	case tagOID:
		return appendTLV(b, tagOID, encodeOID(v.OID))
	}
	return appendTLV(b, v.Tag, nil) // Null and the v2 exceptions
}

func appendVarBinds(b []byte, vbs []VarBind) []byte {
	var list []byte
	for _, vb := range vbs {
		var one []byte
		one = appendTLV(one, tagOID, encodeOID(vb.Name))
		one = appendValue(one, vb.Value)
		list = appendTLV(list, tagSequence, one)
	}
	return appendTLV(b, tagSequence, list)
}

// PDU is a protocol data unit.
type PDU struct {
	Type      byte
	RequestID int32
	ErrStatus int // or non-repeaters for GetBulk
	ErrIndex  int // or max-repetitions for GetBulk
	VarBinds  []VarBind
}

func (p PDU) encode() []byte {
	var c []byte
	c = appendInt(c, int64(p.RequestID))
	c = appendInt(c, int64(p.ErrStatus))
	c = appendInt(c, int64(p.ErrIndex))
	c = appendVarBinds(c, p.VarBinds)
	return appendTLV(nil, p.Type, c)
}

// --- decoding ---

// decoder reads TLVs from a buffer. Offsets are relative to the start of
// the whole message, so callers can find fields (the v3 auth digest).
type decoder struct {
	buf []byte
	pos int
	end int
}

func newDecoder(b []byte) *decoder { return &decoder{buf: b, end: len(b)} }

func (d *decoder) more() bool { return d.pos < d.end }

// next reads one TLV and returns its tag, content and the content's offset.
func (d *decoder) next() (tag byte, content []byte, off int, err error) {
	if d.pos+2 > d.end {
		return 0, nil, 0, errMalformed
	}
	tag = d.buf[d.pos]
	if tag&0x1f == 0x1f {
		return 0, nil, 0, errMalformed // multi-byte tags are not used in SNMP
	}
	l := int(d.buf[d.pos+1])
	p := d.pos + 2
	if l&0x80 != 0 {
		n := l & 0x7f
		if n == 0 || n > 4 || p+n > d.end {
			return 0, nil, 0, errMalformed
		}
		l = 0
		for i := 0; i < n; i++ {
			l = l<<8 | int(d.buf[p+i])
		}
		p += n
	}
	if l < 0 || p+l > d.end {
		return 0, nil, 0, errMalformed
	}
	d.pos = p + l
	return tag, d.buf[p : p+l], p, nil
}

// sub returns a decoder over a constructed TLV's content.
func (d *decoder) sub(want byte) (*decoder, error) {
	tag, _, off, err := d.next()
	if err != nil {
		return nil, err
	}
	if tag != want {
		return nil, errMalformed
	}
	return &decoder{buf: d.buf, pos: off, end: d.pos}, nil
}

func (d *decoder) int() (int64, error) {
	tag, c, _, err := d.next()
	if err != nil {
		return 0, err
	}
	if tag != tagInteger {
		return 0, errMalformed
	}
	return decodeInt(c)
}

func decodeInt(c []byte) (int64, error) {
	if len(c) == 0 || len(c) > 8 {
		return 0, errMalformed
	}
	v := int64(int8(c[0]))
	for _, b := range c[1:] {
		v = v<<8 | int64(b)
	}
	return v, nil
}

func decodeUint(c []byte) (uint64, error) {
	if len(c) == 0 || len(c) > 9 || (len(c) == 9 && c[0] != 0) {
		return 0, errMalformed
	}
	var v uint64
	for _, b := range c {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

func (d *decoder) octets() ([]byte, int, error) {
	tag, c, off, err := d.next()
	if err != nil {
		return nil, 0, err
	}
	if tag != tagOctetString {
		return nil, 0, errMalformed
	}
	return c, off, nil
}

func decodeOID(c []byte) (OID, error) {
	if len(c) == 0 {
		return nil, errMalformed
	}
	var o OID
	var n uint64
	for i, b := range c {
		n = n<<7 | uint64(b&0x7f)
		if n > 0xffffffff {
			return nil, errMalformed
		}
		if b&0x80 != 0 {
			if i == len(c)-1 {
				return nil, errMalformed
			}
			continue
		}
		if o == nil {
			switch {
			case n < 40:
				o = OID{0, uint32(n)}
			case n < 80:
				o = OID{1, uint32(n - 40)}
			default:
				o = OID{2, uint32(n - 80)}
			}
		} else {
			o = append(o, uint32(n))
		}
		n = 0
	}
	if len(o) > 128 {
		return nil, errMalformed
	}
	return o, nil
}

func decodeValue(tag byte, c []byte) (Value, error) {
	v := Value{Tag: tag}
	var err error
	switch tag {
	case tagInteger:
		v.Int, err = decodeInt(c)
	case tagCounter32, tagGauge32, tagTimeTicks, tagCounter64:
		var u uint64
		u, err = decodeUint(c)
		v.Int = int64(u)
	case tagOctetString, tagIPAddress:
		v.Bytes = c
	case tagOID:
		v.OID, err = decodeOID(c)
	case tagNull, tagNoSuchObject, tagNoSuchInstance, tagEndOfMibView:
	default:
		v.Bytes = c
	}
	return v, err
}

// decodePDU reads a PDU (any of the PDU tags).
func (d *decoder) pdu() (PDU, error) {
	tag, _, off, err := d.next()
	if err != nil {
		return PDU{}, err
	}
	if tag < pduGet || tag > pduReport || tag == pduTrapV1 {
		return PDU{}, errMalformed
	}
	p := PDU{Type: tag}
	s := &decoder{buf: d.buf, pos: off, end: d.pos}
	id, err := s.int()
	if err != nil {
		return p, err
	}
	p.RequestID = int32(id)
	es, err := s.int()
	if err != nil {
		return p, err
	}
	ei, err := s.int()
	if err != nil {
		return p, err
	}
	p.ErrStatus, p.ErrIndex = int(es), int(ei)
	list, err := s.sub(tagSequence)
	if err != nil {
		return p, err
	}
	for list.more() {
		vb, err := list.sub(tagSequence)
		if err != nil {
			return p, err
		}
		tag, c, _, err := vb.next()
		if err != nil || tag != tagOID {
			return p, errMalformed
		}
		name, err := decodeOID(c)
		if err != nil {
			return p, err
		}
		vtag, vc, _, err := vb.next()
		if err != nil {
			return p, err
		}
		val, err := decodeValue(vtag, vc)
		if err != nil {
			return p, err
		}
		p.VarBinds = append(p.VarBinds, VarBind{Name: name, Value: val})
		if len(p.VarBinds) > maxVarBinds {
			return p, errMalformed
		}
	}
	return p, nil
}

const maxVarBinds = 256
