package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/duskglow/currently/pkg/sdk"
)

// Megatec (Q1) protocol: many budget and OEM UPSes (Mustek, Powercom,
// Fenton, Atlantis, Ablerex, ...). Commands end with a carriage return;
// replies start with "(" or "#". Unsupported commands are echoed back.

type megatec struct {
	o     Options
	cells int // battery cells, from the rating or the cells option
}

func (m *megatec) name() string { return "megatec" }

func (m *megatec) detect(l *link) bool {
	// A carriage return ends whatever is in the UPS's input buffer (such
	// as the "Y" an APC Smart probe sent), so Q1 arrives clean.
	_, _ = l.ask("\r", "\r")
	r, err := l.ask("Q1\r", "\r")
	return err == nil && strings.HasPrefix(r, "(")
}

// static reads the rating (F) and identity (I).
func (m *megatec) static(l *link) map[string]string {
	vars := map[string]string{}
	if r, err := l.ask("F\r", "\r"); err == nil && strings.HasPrefix(r, "#") {
		f := strings.Fields(r[1:])
		set := func(i int, name string) {
			if i < len(f) && f[i] != "" && !strings.Contains(f[i], "@") {
				if x, err := strconv.ParseFloat(f[i], 64); err == nil {
					vars[name] = fmtNum(x)
				}
			}
		}
		set(0, "input.voltage.nominal")
		set(1, "input.current.nominal")
		set(2, "battery.voltage.nominal")
		set(3, "input.frequency.nominal")
		if nominal, err := strconv.ParseFloat(vars["battery.voltage.nominal"], 64); err == nil && m.cells == 0 {
			m.cells = int(math.Round(nominal / 2)) // lead-acid: 2 V per cell
		}
	}
	if r, err := l.ask("I\r", "\r"); err == nil && strings.HasPrefix(r, "#") && len(r) > 1 {
		// #Company_Name(15) UPS_Model(10) Version(10), space-separated
		s := r[1:]
		field := func(from, to int) string {
			if from >= len(s) {
				return ""
			}
			return strings.TrimSpace(strings.ReplaceAll(s[from:min(to, len(s))], "@", ""))
		}
		if v := field(0, 15); v != "" {
			vars["device.mfr"] = v
		}
		if v := field(16, 26); v != "" {
			vars["device.model"] = v
		}
		if v := field(27, 37); v != "" {
			vars["ups.firmware"] = v
		}
	}
	return vars
}

// poll reads the status (Q1).
func (m *megatec) poll(l *link) (map[string]string, error) {
	r, err := l.ask("Q1\r", "\r")
	if err != nil {
		return nil, err
	}
	return m.parseQ1(r)
}

// parseQ1 decodes "(MMM.M NNN.N PPP.P QQQ RR.R S.SS TT.T b7b6b5b4b3b2b1b0".
func (m *megatec) parseQ1(r string) (map[string]string, error) {
	if !strings.HasPrefix(r, "(") {
		return nil, fmt.Errorf("unexpected Q1 reply %q", r)
	}
	f := strings.Fields(r[1:])
	if len(f) != 8 || len(f[7]) != 8 {
		return nil, fmt.Errorf("unexpected Q1 reply %q", r)
	}
	vars := map[string]string{}
	num := func(i int) (float64, bool) {
		x, err := strconv.ParseFloat(f[i], 64)
		return x, err == nil
	}
	for i, name := range []string{"input.voltage", "input.voltage.fault", "output.voltage", "ups.load", "input.frequency"} {
		if x, ok := num(i); ok {
			vars[name] = fmtNum(x)
		}
	}
	if x, ok := num(6); ok {
		vars["ups.temperature"] = fmtNum(x)
	}
	if v, ok := num(5); ok {
		// On-line units report volts per cell.
		if v < 3 && m.cells > 0 {
			v *= float64(m.cells)
		}
		if v >= 3 || m.cells > 0 {
			vars["battery.voltage"] = fmtNum(math.Round(v*100) / 100)
			if c, ok := m.charge(v); ok {
				vars["battery.charge"] = fmtNum(c)
			}
		}
	}

	bits := f[7]
	on := func(b int) bool { return bits[7-b] == '1' }
	var st sdk.Status
	if on(7) {
		st = append(st, sdk.FlagOnBattery, sdk.FlagDischarging)
	} else {
		st = append(st, sdk.FlagOnline)
	}
	if on(6) {
		st = append(st, sdk.FlagLowBattery)
	}
	if on(5) {
		switch in, out := vars["input.voltage"], vars["output.voltage"]; {
		case !on(3):
			st = append(st, sdk.FlagBypass)
		case parse(in) < parse(out):
			st = append(st, sdk.FlagBoost)
		default:
			st = append(st, sdk.FlagTrim)
		}
	}
	if on(4) {
		st = append(st, sdk.FlagAlarm)
		vars["ups.alarm"] = "UPS failed"
	}
	if on(2) {
		st = append(st, sdk.FlagTest)
	}
	if on(3) {
		vars["ups.type"] = "offline / line interactive"
	} else {
		vars["ups.type"] = "online"
	}
	if on(0) {
		vars["ups.beeper.status"] = "enabled"
	} else {
		vars["ups.beeper.status"] = "disabled"
	}
	vars["ups.status"] = st.String()
	return vars, nil
}

// charge estimates the charge from the battery voltage. Megatec UPSes do
// not report it.
func (m *megatec) charge(v float64) (float64, bool) {
	low, high := 0.0, 0.0
	if m.o.BatteryVoltageLow != nil && m.o.BatteryVoltageHigh != nil {
		low, high = *m.o.BatteryVoltageLow, *m.o.BatteryVoltageHigh
	} else if m.cells > 0 {
		low, high = 1.75*float64(m.cells), 2.2*float64(m.cells)
	}
	if high <= low {
		return 0, false
	}
	return math.Round(math.Min(100, math.Max(0, (v-low)/(high-low)*100))), true
}

func parse(s string) float64 { x, _ := strconv.ParseFloat(s, 64); return x }

func fmtNum(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }

func (m *megatec) commands() []string {
	return []string{"beeper.disable", "beeper.enable", "beeper.toggle", "shutdown.return", "shutdown.stayoff",
		"shutdown.stop", "test.battery.start", "test.battery.start.deep", "test.battery.start.quick", "test.battery.stop"}
}

// megatecDelay encodes a delay for S<n>: .2 to .9 (tenths of a minute),
// or 01 to 10 minutes.
func megatecDelay(param string, def int) (string, error) {
	secs := def
	if strings.TrimSpace(param) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(param))
		if err != nil {
			return "", fmt.Errorf("%w: delay %q must be a number of seconds", sdk.ErrInvalid, param)
		}
		secs = n
	}
	switch {
	case secs >= 12 && secs <= 54 && secs%6 == 0:
		return "." + strconv.Itoa(secs/6), nil
	case secs >= 60 && secs <= 600 && secs%60 == 0:
		return fmt.Sprintf("%02d", secs/60), nil
	}
	return "", fmt.Errorf("%w: this UPS can delay a shutdown by 12 to 54 seconds in steps of 6, or 1 to 10 whole minutes, not %d seconds", sdk.ErrInvalid, secs)
}

func (m *megatec) run(l *link, name, param string, last map[string]string) error {
	var cmd string
	switch name {
	case "test.battery.start.quick":
		cmd = "T"
	case "test.battery.start":
		cmd = "T"
		if p := strings.TrimSpace(param); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n < 1 || n > 99 {
				return fmt.Errorf("%w: test length must be 1 to 99 minutes", sdk.ErrInvalid)
			}
			cmd = fmt.Sprintf("T%02d", n)
		}
	case "test.battery.start.deep":
		cmd = "TL"
	case "test.battery.stop":
		cmd = "CT"
	case "beeper.toggle":
		cmd = "Q"
	case "beeper.enable", "beeper.disable":
		want := strings.TrimPrefix(name, "beeper.") + "d"
		if last["ups.beeper.status"] == want {
			return nil
		}
		cmd = "Q"
	case "shutdown.return", "shutdown.stayoff":
		d, err := megatecDelay(param, 60)
		if err != nil {
			return err
		}
		// R0003: restore 3 minutes after shutting down, or when mains
		// returns (R0001 and R0002 can leave early firmware off for good).
		// R9999: stay off for about a week.
		cmd = "S" + d + "R0003"
		if name == "shutdown.stayoff" {
			cmd = "S" + d + "R9999"
		}
	case "shutdown.stop":
		cmd = "C"
	default:
		return sdk.ErrNotSupported
	}
	// Commands have no reply; an echo means the UPS does not support it.
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.send(cmd + "\r"); err != nil {
		return err
	}
	echo, err := l.readUntil("\r", 300*time.Millisecond)
	if err == nil && strings.TrimSpace(echo) == cmd {
		return fmt.Errorf("%w: the UPS rejected %q", sdk.ErrNotSupported, cmd)
	}
	if err != nil && !errors.Is(err, errTimeout) {
		return err
	}
	return nil
}
