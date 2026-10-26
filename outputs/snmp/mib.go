package snmp

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// The UPS-MIB (RFC 1628), filled from NUT variables, plus the parts of
// SNMPv2-MIB and SNMP-FRAMEWORK-MIB that monitoring tools expect.

var (
	oidSystem      = mustOID("1.3.6.1.2.1.1")
	oidSysUpTime   = mustOID("1.3.6.1.2.1.1.3.0")
	oidUPSMIB      = mustOID("1.3.6.1.2.1.33")
	oidIdent       = mustOID("1.3.6.1.2.1.33.1.1")
	oidBattery     = mustOID("1.3.6.1.2.1.33.1.2")
	oidInput       = mustOID("1.3.6.1.2.1.33.1.3")
	oidOutput      = mustOID("1.3.6.1.2.1.33.1.4")
	oidBypass      = mustOID("1.3.6.1.2.1.33.1.5")
	oidAlarm       = mustOID("1.3.6.1.2.1.33.1.6")
	oidWellKnown   = mustOID("1.3.6.1.2.1.33.1.6.3")
	oidConfig      = mustOID("1.3.6.1.2.1.33.1.9")
	oidUPSTraps    = mustOID("1.3.6.1.2.1.33.2")
	oidSnmpEngine  = mustOID("1.3.6.1.6.3.10.2.1")
	oidSnmpTrapOID = mustOID("1.3.6.1.6.3.1.1.4.1.0")

	oidIdentName        = oidIdent.Append(5, 0)
	oidMinutesRemaining = oidBattery.Append(3, 0)
	oidSecondsOnBattery = oidBattery.Append(2, 0)
	oidLowBattTime      = oidConfig.Append(7, 0)
	oidAlarmID          = oidAlarm.Append(2, 1, 1)
	oidAlarmDescr       = oidAlarm.Append(2, 1, 2)
)

// Well-known alarms (upsWellKnownAlarms), by their last arc.
const (
	alarmBatteryBad         = 1
	alarmOnBattery          = 2
	alarmLowBattery         = 3
	alarmOutputOverload     = 8
	alarmOnBypass           = 9
	alarmUpsOutputOff       = 14
	alarmGeneralFault       = 18
	alarmCommunicationsLost = 20
	alarmShutdownPending    = 22
	alarmTestInProgress     = 24
)

// alarmsFor returns the well-known alarms that apply to a UPS, in order.
func alarmsFor(u sdk.UPS) []uint32 {
	if u.State != sdk.StateConnected {
		return []uint32{alarmCommunicationsLost}
	}
	st := u.Status()
	var out []uint32
	for _, m := range []struct {
		flag  sdk.Flag
		alarm uint32
	}{
		{sdk.FlagReplaceBattery, alarmBatteryBad},
		{sdk.FlagOnBattery, alarmOnBattery},
		{sdk.FlagLowBattery, alarmLowBattery},
		{sdk.FlagOverload, alarmOutputOverload},
		{sdk.FlagBypass, alarmOnBypass},
		{sdk.FlagOff, alarmUpsOutputOff},
		{sdk.FlagAlarm, alarmGeneralFault},
		{sdk.FlagForcedShutdown, alarmShutdownPending},
	} {
		if st.Has(m.flag) {
			out = append(out, m.alarm)
		}
	}
	if st.Has(sdk.FlagTest) || st.Has(sdk.FlagCalibrating) {
		out = append(out, alarmTestInProgress)
	}
	return out
}

// mibBuilder collects the objects of one UPS.
type mibBuilder struct {
	u   sdk.UPS
	vbs []VarBind
}

func (b *mibBuilder) add(o OID, v Value) { b.vbs = append(b.vbs, VarBind{o, v}) }

func (b *mibBuilder) num(name string) (float64, bool) {
	f, ok := b.u.Float(name)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// intVar adds an INTEGER from a variable, multiplied by scale (UPS-MIB
// reports some values in tenths). Missing variables are left out.
func (b *mibBuilder) intVar(o OID, name string, scale float64) {
	if f, ok := b.num(name); ok {
		b.add(o, intValue(clampInt(f*scale)))
	}
}

func (b *mibBuilder) str(o OID, s string, max int) {
	if len(s) > max {
		s = s[:max]
	}
	b.add(o, stringValue(s))
}

func clampInt(f float64) int64 {
	f = math.Round(f)
	switch {
	case f > math.MaxInt32:
		return math.MaxInt32
	case f < math.MinInt32:
		return math.MinInt32
	}
	return int64(f)
}

func nonNeg(f float64) int64 { return max(clampInt(f), 0) }

// upsTree returns the UPS-MIB objects for u, sorted.
func upsTree(u sdk.UPS, t *upsTrack, agentVersion string, now time.Time) []VarBind {
	b := &mibBuilder{u: u}
	st := u.Status()
	connected := u.State == sdk.StateConnected

	// upsIdent
	b.str(oidIdent.Append(1, 0), firstOf(u.Value("device.mfr"), u.Value("ups.mfr")), 31)
	b.str(oidIdent.Append(2, 0), firstOf(u.Value("device.model"), u.Value("ups.model")), 63)
	b.str(oidIdent.Append(3, 0), u.Value("ups.firmware"), 63)
	b.str(oidIdent.Append(4, 0), "nut-ng "+agentVersion, 63)
	b.str(oidIdent.Append(5, 0), u.Name, 63)
	b.str(oidIdent.Append(6, 0), u.Description, 63)

	if connected {
		// upsBattery
		bs := int64(2) // batteryNormal
		switch {
		case len(st) == 0:
			bs = 1 // unknown
		case st.Has(sdk.FlagLowBattery):
			bs = 3 // batteryLow
		}
		b.add(oidBattery.Append(1, 0), intValue(bs))
		b.add(oidSecondsOnBattery, intValue(t.secondsOnBattery(now)))
		if f, ok := b.num("battery.runtime"); ok {
			b.add(oidMinutesRemaining, intValue(nonNeg(f/60)))
		}
		if f, ok := b.num("battery.charge"); ok {
			b.add(oidBattery.Append(4, 0), intValue(min(nonNeg(f), 100)))
		}
		b.intVar(oidBattery.Append(5, 0), "battery.voltage", 10)
		b.intVar(oidBattery.Append(6, 0), "battery.current", 10)
		b.intVar(oidBattery.Append(7, 0), "battery.temperature", 1)

		// upsInput
		b.add(oidInput.Append(1, 0), counterValue(t.lineBads))
		in := b.lines("input")
		b.add(oidInput.Append(2, 0), intValue(int64(len(in))))
		for i, l := range in {
			row := uint32(i + 1)
			b.add(oidInput.Append(3, 1, 1, row), intValue(int64(row)))
			b.intVar(oidInput.Append(3, 1, 2, row), "input.frequency", 10)
			b.intVar(oidInput.Append(3, 1, 3, row), l.voltage, 1)
			b.intVar(oidInput.Append(3, 1, 4, row), l.current, 10)
			b.intVar(oidInput.Append(3, 1, 5, row), l.realpower, 1)
		}

		// upsOutput
		b.add(oidOutput.Append(1, 0), intValue(outputSource(st)))
		b.intVar(oidOutput.Append(2, 0), "output.frequency", 10)
		out := b.lines("output")
		b.add(oidOutput.Append(3, 0), intValue(int64(len(out))))
		for i, l := range out {
			row := uint32(i + 1)
			b.add(oidOutput.Append(4, 1, 1, row), intValue(int64(row)))
			b.intVar(oidOutput.Append(4, 1, 2, row), l.voltage, 1)
			b.intVar(oidOutput.Append(4, 1, 3, row), l.current, 10)
			b.intVar(oidOutput.Append(4, 1, 4, row), l.realpower, 1)
			b.intVar(oidOutput.Append(4, 1, 5, row), l.load, 1)
		}

		// upsBypass
		b.intVar(oidBypass.Append(1, 0), "input.bypass.frequency", 10)
		if _, ok := b.num("input.bypass.voltage"); ok {
			b.add(oidBypass.Append(2, 0), intValue(1))
			b.add(oidBypass.Append(3, 1, 1, 1), intValue(1))
			b.intVar(oidBypass.Append(3, 1, 2, 1), "input.bypass.voltage", 1)
			b.intVar(oidBypass.Append(3, 1, 3, 1), "input.bypass.current", 10)
			b.intVar(oidBypass.Append(3, 1, 4, 1), "input.bypass.realpower", 1)
		} else {
			b.add(oidBypass.Append(2, 0), intValue(0))
		}
	}

	// upsAlarm
	b.add(oidAlarm.Append(1, 0), gaugeValue(int64(len(t.alarms))))
	for _, a := range t.alarms {
		b.add(oidAlarmID.Append(a.id), intValue(int64(a.id)))
		b.add(oidAlarmDescr.Append(a.id), oidValue(oidWellKnown.Append(a.descr)))
		b.add(oidAlarm.Append(2, 1, 3, a.id), ticksValue(a.since))
	}

	if connected {
		// upsConfig
		b.intVar(oidConfig.Append(1, 0), "input.voltage.nominal", 1)
		b.intVar(oidConfig.Append(2, 0), "input.frequency.nominal", 10)
		b.intVar(oidConfig.Append(3, 0), "output.voltage.nominal", 1)
		b.intVar(oidConfig.Append(4, 0), "output.frequency.nominal", 10)
		b.intVar(oidConfig.Append(5, 0), "ups.power.nominal", 1)
		b.intVar(oidConfig.Append(6, 0), "ups.realpower.nominal", 1)
		if f, ok := b.num("battery.runtime.low"); ok {
			b.add(oidLowBattTime, intValue(nonNeg(f/60)))
		}
		switch strings.ToLower(u.Value("ups.beeper.status")) {
		case "disabled":
			b.add(oidConfig.Append(8, 0), intValue(1))
		case "enabled":
			b.add(oidConfig.Append(8, 0), intValue(2))
		case "muted":
			b.add(oidConfig.Append(8, 0), intValue(3))
		}
		b.intVar(oidConfig.Append(9, 0), "input.transfer.low", 1)
		b.intVar(oidConfig.Append(10, 0), "input.transfer.high", 1)
	}

	slices.SortFunc(b.vbs, func(x, y VarBind) int { return x.Name.Compare(y.Name) })
	return b.vbs
}

// line names the variables of one input or output line (phase).
type line struct{ voltage, current, realpower, load string }

// lines returns the lines of "input" or "output": one for single-phase
// UPSes, or one per phase when <prefix>.phases is 3 (NUT's L1..L3
// variables).
func (b *mibBuilder) lines(prefix string) []line {
	if p, _ := b.num(prefix + ".phases"); p == 3 {
		var out []line
		for i := 1; i <= 3; i++ {
			l := prefix + ".L" + strconv.Itoa(i)
			out = append(out, line{l + "-N.voltage", l + ".current", l + ".realpower", l + ".power.percent"})
		}
		return out
	}
	l := line{prefix + ".voltage", prefix + ".current", prefix + ".realpower", "ups.load"}
	if prefix == "input" {
		l.load = ""
	}
	for _, name := range []string{l.voltage, l.current, l.realpower} {
		if _, ok := b.num(name); ok {
			return []line{l}
		}
	}
	return nil
}

// outputSource returns upsOutputSource for a status.
func outputSource(st sdk.Status) int64 {
	switch {
	case st.Has(sdk.FlagOff):
		return 2 // none
	case st.Has(sdk.FlagBypass):
		return 4 // bypass
	case st.Has(sdk.FlagOnBattery):
		return 5 // battery
	case st.Has(sdk.FlagBoost):
		return 6 // booster
	case st.Has(sdk.FlagTrim):
		return 7 // reducer
	case st.Has(sdk.FlagOnline):
		return 3 // normal
	}
	return 1 // other
}

func firstOf(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
