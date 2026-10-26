package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/duskglow/currently/pkg/sdk"
)

// APC Smart protocol (Smart-UPS, Matrix-UPS and Back-UPS Pro with the
// smart signalling cable). After "Y" puts the UPS in smart mode, each
// query is one character and each reply a line ending in CR LF. The UPS
// also sends single alert characters at any time.

type apcSmart struct {
	missing  map[byte]bool // queries this UPS does not answer
	timeouts map[byte]int  // unanswered queries in a row
	alerts   map[byte]bool // alerts seen since the last poll
}

func newAPCSmart() *apcSmart {
	return &apcSmart{missing: map[byte]bool{}, timeouts: map[byte]int{}, alerts: map[byte]bool{}}
}

func (a *apcSmart) name() string { return "apcsmart" }

const alertChars = "!$%+?=*#&|"

func (a *apcSmart) detect(l *link) bool {
	r, err := l.ask("Y", "\r\n")
	return err == nil && strings.HasSuffix(a.strip(r), "SM")
}

// strip removes alert characters the UPS sent before a reply.
func (a *apcSmart) strip(r string) string {
	for len(r) > 0 && strings.IndexByte(alertChars, r[0]) >= 0 && r != "*" {
		a.alerts[r[0]] = true
		r = r[1:]
	}
	return r
}

// query asks one question; ok is false for "NA" or no answer.
func (a *apcSmart) query(l *link, c byte) (string, bool) {
	if a.missing[c] {
		return "", false
	}
	r, err := l.ask(string(c), "\r\n")
	r = strings.TrimSpace(a.strip(r))
	switch {
	case r == "NA":
		a.missing[c] = true
		return "", false
	case err != nil || r == "":
		// Older models stay silent on queries they lack.
		a.timeouts[c]++
		a.missing[c] = a.timeouts[c] >= 3
		return "", false
	}
	a.timeouts[c] = 0
	return r, true
}

func num(r string) (string, bool) {
	x, err := strconv.ParseFloat(strings.TrimSuffix(r, ":"), 64)
	if err != nil {
		return "", false
	}
	return fmtNum(x), true
}

func (a *apcSmart) static(l *link) map[string]string {
	vars := map[string]string{"device.mfr": "APC"}
	text := map[byte]string{0x01: "device.model", 'n': "device.serial", 'b': "ups.firmware",
		'm': "ups.mfr.date", 'x': "battery.date"}
	for _, c := range []byte{0x01, 'n', 'b', 'm', 'x'} {
		if r, ok := a.query(l, c); ok {
			vars[text[c]] = r
		}
	}
	if _, ok := vars["ups.firmware"]; !ok {
		if r, ok := a.query(l, 'V'); ok {
			vars["ups.firmware"] = r
		}
	}
	for c, name := range map[byte]string{'g': "battery.voltage.nominal", 'l': "input.transfer.low",
		'u': "input.transfer.high", 'p': "ups.delay.shutdown"} {
		if r, ok := a.query(l, c); ok {
			if v, ok := num(r); ok {
				vars[name] = v
			}
		}
	}
	return vars
}

func (a *apcSmart) poll(l *link) (map[string]string, error) {
	vars := map[string]string{}
	// The status comes first: without it, the UPS is not answering.
	r, ok := a.query(l, 'Q')
	if !ok {
		delete(a.missing, 'Q')
		// It may have left smart mode (after a power cycle).
		if !a.detect(l) {
			return nil, errTimeout
		}
		if r, ok = a.query(l, 'Q'); !ok {
			delete(a.missing, 'Q')
			return nil, errTimeout
		}
	}
	q, err := strconv.ParseUint(r, 16, 8)
	if err != nil {
		return nil, fmt.Errorf("unexpected status reply %q", r)
	}
	vars["ups.status"] = apcStatus(byte(q)).String()

	for c, name := range map[byte]string{'f': "battery.charge", 'L': "input.voltage", 'O': "output.voltage",
		'P': "ups.load", 'F': "input.frequency", 'B': "battery.voltage", 'C': "ups.temperature"} {
		if r, ok := a.query(l, c); ok {
			if v, ok := num(r); ok {
				vars[name] = v
			}
		}
	}
	if r, ok := a.query(l, 'j'); ok { // minutes, as "0327:"
		if m, err := strconv.Atoi(strings.TrimSuffix(r, ":")); err == nil {
			vars["battery.runtime"] = strconv.Itoa(m * 60)
		}
	}
	if r, ok := a.query(l, 'X'); ok {
		vars["ups.test.result"] = map[string]string{"OK": "Passed", "BT": "Failed: battery capacity",
			"NG": "Failed: overload", "NO": "No recent test"}[r]
		if vars["ups.test.result"] == "" {
			vars["ups.test.result"] = r
		}
	}
	if a.alerts['#'] {
		vars["ups.alarm"] = "Replace battery"
	}
	clear(a.alerts)
	return vars, nil
}

// apcStatus decodes the Q status byte.
func apcStatus(q byte) sdk.Status {
	var st sdk.Status
	for _, b := range []struct {
		bit  byte
		flag sdk.Flag
	}{
		{0x08, sdk.FlagOnline}, {0x10, sdk.FlagOnBattery}, {0x40, sdk.FlagLowBattery},
		{0x80, sdk.FlagReplaceBattery}, {0x20, sdk.FlagOverload}, {0x04, sdk.FlagBoost},
		{0x02, sdk.FlagTrim}, {0x01, sdk.FlagCalibrating},
	} {
		if q&b.bit != 0 {
			st = append(st, b.flag)
		}
	}
	if q&0x10 != 0 {
		st = append(st, sdk.FlagDischarging)
	}
	return st
}

func (a *apcSmart) commands() []string {
	return []string{"load.off", "shutdown.return", "shutdown.stayoff", "shutdown.stop", "test.battery.start",
		"test.panel.start"}
}

func (a *apcSmart) run(l *link, name, param string, last map[string]string) error {
	expectOK := func(cmd string) error {
		r, err := l.ask(cmd, "\r\n")
		r = strings.TrimSpace(a.strip(r))
		if err != nil {
			return fmt.Errorf("no reply to %q: %w", cmd, err)
		}
		if r != "OK" && r != "*" {
			return fmt.Errorf("%w: the UPS answered %q", sdk.ErrNotSupported, r)
		}
		return nil
	}
	// twice sends a command character twice, 1.5 s apart, as the UPS
	// requires for K and Z.
	twice := func(c string) error {
		l.mu.Lock()
		defer l.mu.Unlock()
		if err := l.send(c); err != nil {
			return err
		}
		time.Sleep(1600 * time.Millisecond)
		return l.send(c)
	}
	switch name {
	case "test.battery.start":
		return expectOK("W")
	case "test.panel.start":
		return expectOK("A")
	case "shutdown.return":
		// The UPS waits its own grace delay (p); it cannot take another.
		if p := strings.TrimSpace(param); p != "" {
			grace, _ := a.query(l, 'p')
			g, _ := num(grace)
			if s, _ := num(p); g != "" && s != g {
				return fmt.Errorf("%w: this UPS shuts down after its grace delay of %s seconds; set the delay (shutdown.ups_off_delay) to %s",
					sdk.ErrInvalid, g, g)
			}
		}
		if strings.Contains(last["ups.status"], string(sdk.FlagOnBattery)) {
			return expectOK("S") // soft shutdown: back on when mains returns
		}
		// On line, S does nothing: shut down and wake after 0 hours.
		return expectOK("@000")
	case "shutdown.stayoff":
		return twice("K")
	case "shutdown.stop":
		return expectOK("\x7f")
	case "load.off":
		return twice("Z")
	}
	return sdk.ErrNotSupported
}
