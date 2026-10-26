package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/duskglow/currently/pkg/sdk"
)

// A mib maps one MIB's objects to NUT variables, derives ups.status, and
// knows the SNMP SETs behind each instant command.
type mib struct {
	name   string
	fields []field
	// status builds ups.status (and related variables) from the raw values.
	status func(raw values, vars map[string]string)
	// commands maps command names to the SETs that run them.
	commands map[string]func(param string, raw values) ([]set, error)
}

// field is one object. Later fields override earlier ones for the same
// variable, so high-precision objects follow their basic versions.
type field struct {
	oid  string
	name string // NUT variable; empty for objects only status() reads
	kind kind
	div  float64          // numbers: divide by this (10 for tenths)
	enum map[int64]string // kindEnum: value names
}

type kind int

const (
	kindNumber  kind = iota
	kindString       // OCTET STRING
	kindTicks        // TimeTicks (hundredths of a second) → seconds
	kindMinutes      // minutes → seconds
	kindEnum         // integer → name
)

// set is one SNMP SET.
type set struct {
	oid   string
	value any // int, uint32 (TimeTicks) or string (an OID)
	ticks bool
	isOID bool
}

// values holds raw polled values by OID (with a leading dot).
type values map[string]any

func (v values) int(oid string) (int64, bool) {
	switch x := v[oid].(type) {
	case int64:
		return x, true
	case uint64:
		return int64(x), true
	}
	return 0, false
}

// delaySeconds parses a command's delay parameter (seconds), with a default.
func delaySeconds(param string, def int) (int, error) {
	if strings.TrimSpace(param) == "" {
		return def, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(param))
	if err != nil || n < 0 || n > 86400 {
		return 0, fmt.Errorf("%w: delay %q must be a number of seconds", sdk.ErrInvalid, param)
	}
	return n, nil
}

// --- UPS-MIB (RFC 1628) ---

const ietf = ".1.3.6.1.2.1.33.1"

var ietfMIB = &mib{
	name: "ietf",
	fields: []field{
		{oid: ietf + ".1.1.0", name: "device.mfr", kind: kindString},
		{oid: ietf + ".1.2.0", name: "device.model", kind: kindString},
		{oid: ietf + ".1.3.0", name: "ups.firmware", kind: kindString},
		{oid: ietf + ".2.1.0"}, // upsBatteryStatus
		{oid: ietf + ".2.3.0", name: "battery.runtime", kind: kindMinutes},
		{oid: ietf + ".2.4.0", name: "battery.charge"},
		{oid: ietf + ".2.5.0", name: "battery.voltage", div: 10},
		{oid: ietf + ".2.6.0", name: "battery.current", div: 10},
		{oid: ietf + ".2.7.0", name: "battery.temperature"},
		{oid: ietf + ".3.3.1.2.1", name: "input.frequency", div: 10},
		{oid: ietf + ".3.3.1.3.1", name: "input.voltage"},
		{oid: ietf + ".3.3.1.4.1", name: "input.current", div: 10},
		{oid: ietf + ".3.3.1.5.1", name: "input.realpower"},
		{oid: ietf + ".4.1.0"}, // upsOutputSource
		{oid: ietf + ".4.2.0", name: "output.frequency", div: 10},
		{oid: ietf + ".4.4.1.2.1", name: "output.voltage"},
		{oid: ietf + ".4.4.1.3.1", name: "output.current", div: 10},
		{oid: ietf + ".4.4.1.4.1", name: "ups.realpower"},
		{oid: ietf + ".4.4.1.5.1", name: "ups.load"},
		{oid: ietf + ".5.1.0", name: "input.bypass.frequency", div: 10},
		{oid: ietf + ".5.3.1.2.1", name: "input.bypass.voltage"},
		{oid: ietf + ".6.1.0"}, // upsAlarmsPresent
		{oid: ietf + ".7.3.0", name: "ups.test.result", kind: kindEnum, enum: map[int64]string{
			1: "Done and passed", 2: "Done and warning", 3: "Done and error", 4: "Aborted",
			5: "In progress", 6: "No test initiated"}},
		{oid: ietf + ".9.1.0", name: "input.voltage.nominal"},
		{oid: ietf + ".9.2.0", name: "input.frequency.nominal", div: 10},
		{oid: ietf + ".9.3.0", name: "output.voltage.nominal"},
		{oid: ietf + ".9.4.0", name: "output.frequency.nominal", div: 10},
		{oid: ietf + ".9.5.0", name: "ups.power.nominal"},
		{oid: ietf + ".9.6.0", name: "ups.realpower.nominal"},
		{oid: ietf + ".9.7.0", name: "battery.runtime.low", kind: kindMinutes},
		{oid: ietf + ".9.8.0", name: "ups.beeper.status", kind: kindEnum, enum: map[int64]string{
			1: "disabled", 2: "enabled", 3: "muted"}},
		{oid: ietf + ".9.9.0", name: "input.transfer.low"},
		{oid: ietf + ".9.10.0", name: "input.transfer.high"},
	},
	status: ietfStatus,
	commands: map[string]func(string, values) ([]set, error){
		"test.battery.start":       ietfTest(3),
		"test.battery.start.quick": ietfTest(3),
		"test.battery.start.deep":  ietfTest(4),
		"test.battery.stop":        ietfTest(2),
		"shutdown.return": func(p string, _ values) ([]set, error) {
			d, err := delaySeconds(p, 20)
			return []set{{oid: ietf + ".8.5.0", value: 1}, {oid: ietf + ".8.2.0", value: d}}, err
		},
		"shutdown.stayoff": func(p string, _ values) ([]set, error) {
			d, err := delaySeconds(p, 20)
			return []set{{oid: ietf + ".8.5.0", value: 2}, {oid: ietf + ".8.2.0", value: d}}, err
		},
		"load.off": func(string, values) ([]set, error) {
			return []set{{oid: ietf + ".8.5.0", value: 2}, {oid: ietf + ".8.2.0", value: 0}}, nil
		},
		"shutdown.stop": func(string, values) ([]set, error) {
			return []set{{oid: ietf + ".8.2.0", value: -1}}, nil
		},
	},
}

// ietfTest starts a test by setting upsTestId to a upsWellKnownTests OID:
// 2 abort, 3 quick battery test, 4 deep battery calibration.
func ietfTest(test int) func(string, values) ([]set, error) {
	return func(string, values) ([]set, error) {
		return []set{{oid: ietf + ".7.1.0", value: ietf + ".7.7." + strconv.Itoa(test), isOID: true}}, nil
	}
}

// Well-known alarms (upsAlarmDescr values) that become status flags.
var ietfAlarms = map[int64]struct {
	flag sdk.Flag
	text string
}{
	1:  {sdk.FlagReplaceBattery, "Battery bad"},
	4:  {sdk.FlagLowBattery, "Battery depleted"},
	5:  {"", "Temperature out of tolerance"},
	6:  {"", "Input out of tolerance"},
	7:  {"", "Output out of tolerance"},
	8:  {sdk.FlagOverload, "Output overload"},
	9:  {sdk.FlagBypass, "On bypass"},
	10: {"", "Bypass out of tolerance"},
	11: {sdk.FlagOff, "Output off as requested"},
	12: {sdk.FlagOff, "UPS off as requested"},
	13: {"", "Charger failed"},
	14: {sdk.FlagOff, "Output off"},
	15: {sdk.FlagOff, "UPS system off"},
	16: {"", "Fan failure"},
	17: {"", "Fuse failure"},
	18: {"", "General fault"},
	19: {"", "Diagnostic test failed"},
	21: {"", "Awaiting power"},
	// A pending shutdown means the UPS will cut power: shut down, as with
	// NUT's forced shutdown.
	22: {sdk.FlagForcedShutdown, "Shutdown pending"},
	23: {sdk.FlagForcedShutdown, "Shutdown imminent"},
	24: {sdk.FlagTest, "Test in progress"},
}

const ietfAlarmDescr = ietf + ".6.2.1.2"

func ietfStatus(raw values, vars map[string]string) {
	var st sdk.Status
	add := func(f sdk.Flag) {
		if f != "" && !st.Has(f) {
			st = append(st, f)
		}
	}
	switch src, _ := raw.int(ietf + ".4.1.0"); src {
	case 2:
		add(sdk.FlagOff)
	case 3:
		add(sdk.FlagOnline)
	case 4:
		add(sdk.FlagBypass)
	case 5:
		add(sdk.FlagOnBattery)
		add(sdk.FlagDischarging)
	case 6:
		add(sdk.FlagOnline)
		add(sdk.FlagBoost)
	case 7:
		add(sdk.FlagOnline)
		add(sdk.FlagTrim)
	}
	if bs, _ := raw.int(ietf + ".2.1.0"); bs == 3 || bs == 4 {
		add(sdk.FlagLowBattery)
	}
	var texts []string
	for oid, v := range raw {
		if !strings.HasPrefix(oid, ietfAlarmDescr+".") {
			continue
		}
		s, _ := v.(string)
		arc, err := strconv.ParseInt(s[strings.LastIndex(s, ".")+1:], 10, 64)
		if err != nil || !strings.HasPrefix(strings.TrimPrefix(s, "."), strings.TrimPrefix(ietf, ".")+".6.3.") {
			continue
		}
		if a, ok := ietfAlarms[arc]; ok {
			add(a.flag)
			texts = append(texts, a.text)
		}
	}
	if len(texts) > 0 {
		slices.Sort(texts)
		vars["ups.alarm"] = strings.Join(slices.Compact(texts), ", ")
	}
	if len(st) > 0 {
		vars["ups.status"] = st.String()
	}
}

// --- APC PowerNet-MIB ---

const apc = ".1.3.6.1.4.1.318.1.1.1"

var apcMIB = &mib{
	name: "apc",
	fields: []field{
		{oid: apc + ".1.1.1.0", name: "device.model", kind: kindString},
		{oid: apc + ".1.2.1.0", name: "ups.firmware", kind: kindString},
		{oid: apc + ".1.2.2.0", name: "ups.mfr.date", kind: kindString},
		{oid: apc + ".1.2.3.0", name: "device.serial", kind: kindString},
		{oid: apc + ".2.1.1.0"}, // upsBasicBatteryStatus
		{oid: apc + ".2.1.3.0", name: "battery.date", kind: kindString},
		{oid: apc + ".2.2.1.0", name: "battery.charge"},
		{oid: apc + ".2.3.1.0", name: "battery.charge", div: 10},
		{oid: apc + ".2.2.2.0", name: "ups.temperature"},
		{oid: apc + ".2.3.2.0", name: "ups.temperature", div: 10},
		{oid: apc + ".2.2.3.0", name: "battery.runtime", kind: kindTicks},
		{oid: apc + ".2.2.4.0"}, // upsAdvBatteryReplaceIndicator
		{oid: apc + ".2.2.7.0", name: "battery.voltage.nominal"},
		{oid: apc + ".2.2.8.0", name: "battery.voltage"},
		{oid: apc + ".2.3.4.0", name: "battery.voltage", div: 10},
		{oid: apc + ".3.2.1.0", name: "input.voltage"},
		{oid: apc + ".3.3.1.0", name: "input.voltage", div: 10},
		{oid: apc + ".3.2.4.0", name: "input.frequency"},
		{oid: apc + ".3.3.4.0", name: "input.frequency", div: 10},
		{oid: apc + ".3.2.5.0", name: "input.transfer.reason", kind: kindEnum, enum: map[int64]string{
			1: "noTransfer", 2: "highLineVoltage", 3: "brownout", 4: "blackout", 5: "smallMomentarySag",
			6: "deepMomentarySag", 7: "smallMomentarySpike", 8: "largeMomentarySpike", 9: "selfTest",
			10: "rateOfVoltageChange"}},
		{oid: apc + ".4.1.1.0"}, // upsBasicOutputStatus
		{oid: apc + ".4.2.1.0", name: "output.voltage"},
		{oid: apc + ".4.3.1.0", name: "output.voltage", div: 10},
		{oid: apc + ".4.2.2.0", name: "output.frequency"},
		{oid: apc + ".4.3.2.0", name: "output.frequency", div: 10},
		{oid: apc + ".4.2.3.0", name: "ups.load"},
		{oid: apc + ".4.3.3.0", name: "ups.load", div: 10},
		{oid: apc + ".4.2.4.0", name: "output.current"},
		{oid: apc + ".4.3.4.0", name: "output.current", div: 10},
		{oid: apc + ".5.2.1.0", name: "output.voltage.nominal"},
		{oid: apc + ".5.2.2.0", name: "input.transfer.high"},
		{oid: apc + ".5.2.3.0", name: "input.transfer.low"},
		{oid: apc + ".5.2.9.0", name: "ups.delay.start", kind: kindTicks},
		{oid: apc + ".5.2.10.0", name: "ups.delay.shutdown", kind: kindTicks},
		{oid: apc + ".7.2.3.0", name: "ups.test.result", kind: kindEnum, enum: map[int64]string{
			1: "Ok", 2: "Failed", 3: "Invalid test", 4: "In progress"}},
	},
	status: apcStatus,
	commands: map[string]func(string, values) ([]set, error){
		"test.battery.start": apcSet(".7.2.2.0", 2),
		"test.panel.start":   apcSet(".6.2.5.0", 2),
		"calibrate.start":    apcSet(".7.2.5.0", 2),
		"calibrate.stop":     apcSet(".7.2.5.0", 3),
		"load.off":           apcSet(".6.2.1.0", 2),
		"shutdown.stayoff":   apcSet(".6.2.1.0", 3),
		"load.on":            apcSet(".6.2.6.0", 2),
		// Sleep until mains returns (upsBasicControlConserveBattery), after
		// the shutdown delay. APC accepts it only on battery.
		"shutdown.return": func(p string, raw values) ([]set, error) {
			if out, _ := raw.int(apc + ".4.1.1.0"); out != 3 {
				return nil, fmt.Errorf("%w: APC cards accept shutdown.return only while on battery", sdk.ErrNotSupported)
			}
			var sets []set
			if strings.TrimSpace(p) != "" {
				d, err := delaySeconds(p, 0)
				if err != nil {
					return nil, err
				}
				sets = append(sets, set{oid: apc + ".5.2.10.0", value: uint32(d * 100), ticks: true})
			}
			return append(sets, set{oid: apc + ".6.1.1.0", value: 2}), nil
		},
	},
}

func apcSet(suffix string, v int) func(string, values) ([]set, error) {
	return func(string, values) ([]set, error) { return []set{{oid: apc + suffix, value: v}}, nil }
}

// APC upsBasicOutputStatus values and their status flags.
var apcOutput = map[int64][]sdk.Flag{
	2:  {sdk.FlagOnline},
	3:  {sdk.FlagOnBattery, sdk.FlagDischarging},
	4:  {sdk.FlagOnline, sdk.FlagBoost},
	5:  {sdk.FlagOff},
	6:  {sdk.FlagOff},
	7:  {sdk.FlagOff},
	9:  {sdk.FlagBypass},
	10: {sdk.FlagBypass},
	11: {sdk.FlagOff},
	12: {sdk.FlagOnline, sdk.FlagTrim},
	13: {sdk.FlagOnline},
	14: {sdk.FlagOnline},
	15: {sdk.FlagOnline, sdk.FlagTest},
	16: {sdk.FlagBypass},
	17: {sdk.FlagBypass},
	19: {sdk.FlagOnline},
	20: {sdk.FlagOnline},
	21: {sdk.FlagOnline},
	22: {sdk.FlagOnline},
	24: {sdk.FlagOnline},
	25: {sdk.FlagOnline},
	28: {sdk.FlagOnline, sdk.FlagTest},
}

func apcStatus(raw values, vars map[string]string) {
	out, _ := raw.int(apc + ".4.1.1.0")
	st := sdk.Status(slices.Clone(apcOutput[out]))
	if bs, _ := raw.int(apc + ".2.1.1.0"); bs >= 3 && bs <= 5 {
		st = append(st, sdk.FlagLowBattery)
	}
	if ri, _ := raw.int(apc + ".2.2.4.0"); ri == 2 {
		st = append(st, sdk.FlagReplaceBattery)
	}
	if len(st) > 0 {
		vars["ups.status"] = st.String()
	}
	vars["device.mfr"] = "APC"
}

var mibs = map[string]*mib{"ietf": ietfMIB, "apc": apcMIB}
