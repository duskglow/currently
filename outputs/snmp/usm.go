package snmp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"  //nolint:gosec // HMAC-MD5-96 is part of SNMPv3 USM; users choose it
	"crypto/sha1" //nolint:gosec // HMAC-SHA-96, likewise
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"
)

// SNMPv3 with the User-based Security Model (RFC 3414), HMAC-SHA-2
// authentication (RFC 7860) and AES-128 privacy (RFC 3826).

var (
	oidUSMStats        = mustOID("1.3.6.1.6.3.15.1.1")
	oidUnknownContexts = mustOID("1.3.6.1.6.3.12.1.5.0")
)

// usmStats counts rejected v3 messages: unsupportedSecLevels,
// notInTimeWindows, unknownUserNames, unknownEngineIDs, wrongDigests and
// decryptionErrors (usmStats 1-6).
type usmStats [6]int64

const (
	statSecLevel = iota
	statTimeWindow
	statUnknownUser
	statUnknownEngine
	statWrongDigest
	statDecryption
)

const (
	flagAuth       = 1
	flagPriv       = 2
	flagReportable = 4

	securityModelUSM = 3
	timeWindow       = 150 // seconds
)

type authProtocol struct {
	hash   func() hash.Hash
	macLen int
}

var authProtocols = map[string]authProtocol{
	"md5":    {md5.New, 12},
	"sha":    {sha1.New, 12},
	"sha224": {sha256.New224, 16},
	"sha256": {sha256.New, 24},
	"sha384": {sha512.New384, 32},
	"sha512": {sha512.New, 48},
}

// AuthProtocols lists the supported authentication protocols.
var AuthProtocols = []string{"none", "md5", "sha", "sha224", "sha256", "sha384", "sha512"}

// PrivacyProtocols lists the supported privacy protocols.
var PrivacyProtocols = []string{"none", "aes"}

// NormalizeAuth maps aliases ("sha1", "SHA-256") to the names above.
func NormalizeAuth(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "-", ""))
	switch s {
	case "", "noauth":
		return "none"
	case "sha1":
		return "sha"
	}
	return s
}

// NormalizePrivacy maps aliases ("aes128", "AES") to the names above.
func NormalizePrivacy(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "-", ""))
	switch s {
	case "", "nopriv":
		return "none"
	case "aes128", "aescfb", "aescfb128":
		return "aes"
	}
	return s
}

type usmUser struct {
	name    string
	auth    *authProtocol
	authKey []byte
	privKey []byte // AES-128 key; nil without privacy
}

// level is the security level the user requires: 0 noAuthNoPriv, 1
// authNoPriv, 2 authPriv.
func (u *usmUser) level() int {
	switch {
	case u.privKey != nil:
		return 2
	case u.auth != nil:
		return 1
	}
	return 0
}

func newUSMUser(u User, engineID []byte) (*usmUser, error) {
	out := &usmUser{name: u.Name}
	auth := NormalizeAuth(u.Auth)
	if auth != "none" {
		p, ok := authProtocols[auth]
		if !ok {
			return nil, fmt.Errorf("SNMP user %s: unknown auth protocol %q", u.Name, u.Auth)
		}
		if len(u.AuthPassword) < 8 {
			return nil, fmt.Errorf("SNMP user %s: the auth password must have at least 8 characters", u.Name)
		}
		out.auth = &p
		out.authKey = localizeKey(p.hash, u.AuthPassword, engineID)
	}
	switch priv := NormalizePrivacy(u.Privacy); priv {
	case "none":
	case "aes":
		if out.auth == nil {
			return nil, fmt.Errorf("SNMP user %s: privacy needs an auth protocol", u.Name)
		}
		if len(u.PrivacyPassword) < 8 {
			return nil, fmt.Errorf("SNMP user %s: the privacy password must have at least 8 characters", u.Name)
		}
		out.privKey = localizeKey(out.auth.hash, u.PrivacyPassword, engineID)[:16]
	default:
		return nil, fmt.Errorf("SNMP user %s: unsupported privacy protocol %q (use aes)", u.Name, u.Privacy)
	}
	return out, nil
}

// localizeKey turns a password into a key for one engine (RFC 3414 A.2).
func localizeKey(h func() hash.Hash, password string, engineID []byte) []byte {
	hh := h()
	pw := []byte(password)
	buf := make([]byte, 64)
	idx := 0
	for count := 0; count < 1048576; count += 64 {
		for i := range buf {
			buf[i] = pw[idx%len(pw)]
			idx++
		}
		hh.Write(buf)
	}
	ku := hh.Sum(nil)
	hh.Reset()
	hh.Write(ku)
	hh.Write(engineID)
	hh.Write(ku)
	return hh.Sum(nil)
}

func (u *usmUser) mac(msg []byte) []byte {
	m := hmac.New(u.auth.hash, u.authKey)
	m.Write(msg)
	return m.Sum(nil)[:u.auth.macLen]
}

// aesCFB encrypts or decrypts with AES in 128-bit cipher feedback mode.
func aesCFB(key, iv, data []byte, decrypt bool) []byte {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err) // the key is always 16 bytes
	}
	return cfb(block, iv, data, decrypt)
}

func cfb(block cipher.Block, iv, data []byte, decrypt bool) []byte {
	out := make([]byte, len(data))
	reg := append([]byte(nil), iv...)
	ks := make([]byte, block.BlockSize())
	for i := 0; i < len(data); i += len(ks) {
		block.Encrypt(ks, reg)
		n := min(len(ks), len(data)-i)
		for j := 0; j < n; j++ {
			out[i+j] = data[i+j] ^ ks[j]
		}
		if decrypt {
			copy(reg, data[i:i+n])
		} else {
			copy(reg, out[i:i+n])
		}
	}
	return out
}

func (a *Agent) aesIV(boots, engineTime int64, salt []byte) []byte {
	iv := append(be32(uint32(boots)), be32(uint32(engineTime))...)
	return append(iv, salt...)
}

func (a *Agent) engineTime(now time.Time) int64 { return int64(now.Sub(a.start) / time.Second) }

// v3Message is a parsed SNMPv3 message.
type v3Message struct {
	msgID      int64
	maxSize    int64
	flags      byte
	engineID   []byte
	boots      int64
	time       int64
	user       []byte
	authParams []byte
	authOff    int
	privParams []byte
	data       *decoder // the ScopedPDU, or its encrypted form
}

func parseV3(pkt []byte) (*v3Message, error) {
	top, err := newDecoder(pkt).sub(tagSequence)
	if err != nil {
		return nil, err
	}
	if v, err := top.int(); err != nil || v != 3 {
		return nil, errMalformed
	}
	m := &v3Message{}
	gd, err := top.sub(tagSequence)
	if err != nil {
		return nil, err
	}
	if m.msgID, err = gd.int(); err != nil {
		return nil, err
	}
	if m.maxSize, err = gd.int(); err != nil {
		return nil, err
	}
	flags, _, err := gd.octets()
	if err != nil || len(flags) != 1 {
		return nil, errMalformed
	}
	m.flags = flags[0]
	model, err := gd.int()
	if err != nil {
		return nil, err
	}
	if model != securityModelUSM {
		return nil, fmt.Errorf("unsupported security model %d", model)
	}
	sp, spOff, err := top.octets()
	if err != nil {
		return nil, err
	}
	sd, err := (&decoder{buf: pkt, pos: spOff, end: spOff + len(sp)}).sub(tagSequence)
	if err != nil {
		return nil, err
	}
	if m.engineID, _, err = sd.octets(); err != nil {
		return nil, err
	}
	if m.boots, err = sd.int(); err != nil {
		return nil, err
	}
	if m.time, err = sd.int(); err != nil {
		return nil, err
	}
	if m.user, _, err = sd.octets(); err != nil {
		return nil, err
	}
	if m.authParams, m.authOff, err = sd.octets(); err != nil {
		return nil, err
	}
	if m.privParams, _, err = sd.octets(); err != nil {
		return nil, err
	}
	m.data = top
	return m, nil
}

// scopedPDU reads a plaintext ScopedPDU.
func scopedPDU(d *decoder) (ctxName []byte, pdu PDU, err error) {
	s, err := d.sub(tagSequence)
	if err != nil {
		return nil, pdu, err
	}
	if _, _, err = s.octets(); err != nil { // contextEngineID
		return nil, pdu, err
	}
	if ctxName, _, err = s.octets(); err != nil {
		return nil, pdu, err
	}
	pdu, err = s.pdu()
	return ctxName, pdu, err
}

func (a *Agent) handleV3(pkt []byte) ([]byte, error) {
	m, err := parseV3(pkt)
	if err != nil {
		return nil, err
	}
	if m.flags&flagPriv != 0 && m.flags&flagAuth == 0 {
		return nil, errors.New("privacy without authentication")
	}
	level := 0
	if m.flags&flagAuth != 0 {
		level = 1
	}
	if m.flags&flagPriv != 0 {
		level = 2
	}
	reportable := m.flags&flagReportable != 0
	limit := int(min(max(m.maxSize, 484), maxMessage)) - 200

	// report answers with a usmStats counter, unauthenticated unless user
	// is given (the notInTimeWindows report is authenticated so the
	// manager can trust the engine time it carries).
	report := func(stat int, user *usmUser, cause string) ([]byte, error) {
		a.mu.Lock()
		a.stats[stat]++
		n := a.stats[stat]
		a.mu.Unlock()
		if !reportable {
			return nil, errors.New(cause)
		}
		var reqID int32
		if level < 2 {
			if _, p, err := scopedPDU(m.data); err == nil {
				reqID = p.RequestID
			}
		}
		pdu := PDU{Type: pduReport, RequestID: reqID,
			VarBinds: []VarBind{{oidUSMStats.Append(uint32(stat+1), 0), counterValue(n)}}}
		var flags byte
		if user != nil {
			flags = flagAuth
		}
		return a.encodeV3(m.msgID, flags, user, m.user, nil, pdu), nil
	}

	if !equalBytes(m.engineID, a.engineID) {
		return report(statUnknownEngine, nil, "unknown engine ID (discovery)")
	}
	user, ok := a.users[string(m.user)]
	if !ok {
		return report(statUnknownUser, nil, "unknown user "+string(m.user))
	}
	if level > user.level() {
		return report(statSecLevel, nil, "security level not supported by user "+user.name)
	}
	if level >= 1 {
		if len(m.authParams) != user.auth.macLen {
			return report(statWrongDigest, nil, "wrong digest length")
		}
		check := append([]byte(nil), pkt...)
		clear(check[m.authOff : m.authOff+len(m.authParams)])
		if !hmac.Equal(user.mac(check), m.authParams) {
			return report(statWrongDigest, nil, "wrong digest for user "+user.name)
		}
		now := a.engineTime(a.now())
		if m.boots != a.boots || m.time < now-timeWindow || m.time > now+timeWindow {
			return report(statTimeWindow, user, "not in time window")
		}
	}

	var ctxName []byte
	var pdu PDU
	if level == 2 {
		enc, _, err := m.data.octets()
		if err != nil || len(m.privParams) != 8 {
			return report(statDecryption, nil, "bad encrypted PDU")
		}
		plain := aesCFB(user.privKey, a.aesIV(m.boots, m.time, m.privParams), enc, true)
		ctxName, pdu, err = scopedPDU(newDecoder(plain))
		if err != nil {
			return report(statDecryption, nil, "decryption failed")
		}
	} else if ctxName, pdu, err = scopedPDU(m.data); err != nil {
		return nil, err
	}

	ups := a.defaultUPS()
	if len(ctxName) > 0 {
		ups = string(ctxName)
		if _, ok := a.host.GetUPS(ups); !ok {
			a.mu.Lock()
			a.unknownCtx++
			n := a.unknownCtx
			a.mu.Unlock()
			if !reportable {
				return nil, errors.New("unknown context " + ups)
			}
			r := PDU{Type: pduReport, RequestID: pdu.RequestID,
				VarBinds: []VarBind{{oidUnknownContexts, counterValue(n)}}}
			return a.encodeV3(m.msgID, m.flags&(flagAuth|flagPriv), user, m.user, ctxName, r), nil
		}
	}

	var resp PDU
	if level < user.level() {
		resp = PDU{Type: pduResponse, RequestID: pdu.RequestID, ErrStatus: errAuthorize, ErrIndex: 0, VarBinds: pdu.VarBinds}
	} else if resp, ok = a.process(pdu, false, ups, limit); !ok {
		return nil, nil
	}
	return a.encodeV3(m.msgID, m.flags&(flagAuth|flagPriv), user, m.user, ctxName, resp), nil
}

// encodeV3 builds a message. user may be nil when flags has no auth.
func (a *Agent) encodeV3(msgID int64, flags byte, user *usmUser, userName, ctxName []byte, pdu PDU) []byte {
	if user == nil {
		flags = 0
	}
	var scoped []byte
	scoped = appendOctets(scoped, a.engineID)
	scoped = appendOctets(scoped, ctxName)
	scoped = append(scoped, pdu.encode()...)
	scoped = appendTLV(nil, tagSequence, scoped)

	now := a.engineTime(a.now())
	var privParams, data []byte
	if flags&flagPriv != 0 {
		privParams = binary64(a.salt.Add(1))
		data = appendOctets(nil, aesCFB(user.privKey, a.aesIV(a.boots, now, privParams), scoped, false))
	} else {
		data = scoped
	}
	var authParams []byte
	if flags&flagAuth != 0 {
		authParams = make([]byte, user.auth.macLen)
	}

	var sp []byte
	sp = appendOctets(sp, a.engineID)
	sp = appendInt(sp, a.boots)
	sp = appendInt(sp, now)
	sp = appendOctets(sp, userName)
	sp = appendOctets(sp, authParams)
	sp = appendOctets(sp, privParams)
	sp = appendTLV(nil, tagSequence, sp)

	var gd []byte
	gd = appendInt(gd, msgID)
	gd = appendInt(gd, maxMessage)
	gd = appendOctets(gd, []byte{flags})
	gd = appendInt(gd, securityModelUSM)

	var msg []byte
	msg = appendInt(msg, 3)
	msg = appendTLV(msg, tagSequence, gd)
	msg = appendOctets(msg, sp)
	msg = append(msg, data...)
	msg = appendTLV(nil, tagSequence, msg)

	if flags&flagAuth != 0 {
		if m, err := parseV3(msg); err == nil {
			copy(msg[m.authOff:], user.mac(msg))
		}
	}
	return msg
}

func binary64(v uint64) []byte {
	return append(be32(uint32(v>>32)), be32(uint32(v))...)
}

func equalBytes(a, b []byte) bool { return len(a) == len(b) && string(a) == string(b) }
