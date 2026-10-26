package snmp

import (
	"errors"
	"fmt"
	"net"
	"time"
)

// Probe checks that an SNMP agent answers at addr (host:port). It sends
// an SNMPv3 engine discovery request, which every v3 agent answers
// without credentials, and returns the agent's engine ID.
func Probe(addr string, timeout time.Duration) ([]byte, error) {
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	msgID := int64(time.Now().UnixNano() & 0x7fffffff)
	var scoped []byte
	scoped = appendOctets(scoped, nil)
	scoped = appendOctets(scoped, nil)
	scoped = append(scoped, PDU{Type: pduGet, RequestID: int32(msgID)}.encode()...)
	var sp []byte
	sp = appendOctets(sp, nil)
	sp = appendInt(sp, 0)
	sp = appendInt(sp, 0)
	sp = appendOctets(sp, nil)
	sp = appendOctets(sp, nil)
	sp = appendOctets(sp, nil)
	var gd []byte
	gd = appendInt(gd, msgID)
	gd = appendInt(gd, maxMessage)
	gd = appendOctets(gd, []byte{flagReportable})
	gd = appendInt(gd, securityModelUSM)
	var msg []byte
	msg = appendInt(msg, 3)
	msg = appendTLV(msg, tagSequence, gd)
	msg = appendOctets(msg, appendTLV(nil, tagSequence, sp))
	msg = appendTLV(msg, tagSequence, scoped)
	if _, err := conn.Write(appendTLV(nil, tagSequence, msg)); err != nil {
		return nil, err
	}

	buf := make([]byte, maxMessage)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	m, err := parseV3(buf[:n])
	if err != nil {
		return nil, fmt.Errorf("not an SNMPv3 answer: %w", err)
	}
	if m.msgID != msgID {
		return nil, errors.New("answer to another request")
	}
	if _, pdu, err := scopedPDU(m.data); err != nil || pdu.Type != pduReport {
		return nil, errors.New("no discovery report in the answer")
	}
	return m.engineID, nil
}
