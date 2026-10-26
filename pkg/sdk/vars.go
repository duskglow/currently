package sdk

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Variable is one value reported by a UPS, such as battery.charge = "87".
// Values are kept as strings, as in NUT; use Float for numeric values.
type Variable struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Unit     string    `json:"unit,omitempty"`
	Writable bool      `json:"writable,omitempty"`
	Updated  time.Time `json:"updated"`
}

// Float parses the value as a number.
func (v Variable) Float() (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(v.Value), 64)
	return f, err == nil
}

// VarSpec describes a well-known variable.
type VarSpec struct {
	Name        string `json:"name"`
	Unit        string `json:"unit,omitempty"`
	Numeric     bool   `json:"numeric"`
	Description string `json:"description"`
}

// Units used by well-known variables.
const (
	UnitPercent = "%"
	UnitSeconds = "s"
	UnitVolts   = "V"
	UnitAmps    = "A"
	UnitHertz   = "Hz"
	UnitWatts   = "W"
	UnitVA      = "VA"
	UnitCelsius = "°C"
)

// knownVars lists the NUT variables Currently understands. Drivers may report
// others; they are passed through without a unit.
var knownVars = map[string]VarSpec{}

func init() {
	for _, v := range []VarSpec{
		{"device.mfr", "", false, "Device manufacturer"},
		{"device.model", "", false, "Device model"},
		{"device.serial", "", false, "Device serial number"},
		{"device.type", "", false, "Device type (ups, pdu, ...)"},
		{"ups.status", "", false, "Status flags, for example \"OL CHRG\""},
		{"ups.alarm", "", false, "Active alarms"},
		{"ups.mfr", "", false, "UPS manufacturer"},
		{"ups.model", "", false, "UPS model"},
		{"ups.serial", "", false, "UPS serial number"},
		{"ups.firmware", "", false, "UPS firmware version"},
		{"ups.load", UnitPercent, true, "Load on the UPS as a share of its capacity"},
		{"ups.power", UnitVA, true, "Apparent power drawn"},
		{"ups.power.nominal", UnitVA, true, "Rated apparent power"},
		{"ups.realpower", UnitWatts, true, "Real power drawn"},
		{"ups.realpower.nominal", UnitWatts, true, "Rated real power"},
		{"ups.temperature", UnitCelsius, true, "UPS internal temperature"},
		{"ups.test.result", "", false, "Result of the last self-test"},
		{"ups.beeper.status", "", false, "Beeper state (enabled, disabled, muted)"},
		{"ups.delay.shutdown", UnitSeconds, true, "Delay before the UPS cuts its output after a shutdown command"},
		{"ups.delay.start", UnitSeconds, true, "Delay before the UPS restores output after power returns"},
		{"ups.timer.shutdown", UnitSeconds, true, "Time left before a pending shutdown (-1: none)"},
		{"ups.timer.start", UnitSeconds, true, "Time left before a pending restart (-1: none)"},
		{"ups.vendorid", "", false, "USB vendor ID"},
		{"ups.productid", "", false, "USB product ID"},
		{"battery.charge", UnitPercent, true, "Battery charge"},
		{"battery.charge.low", UnitPercent, true, "Charge level at which the UPS reports low battery"},
		{"battery.charge.warning", UnitPercent, true, "Charge level at which the UPS warns"},
		{"battery.runtime", UnitSeconds, true, "Estimated runtime left on battery"},
		{"battery.runtime.low", UnitSeconds, true, "Runtime at which the UPS reports low battery"},
		{"battery.voltage", UnitVolts, true, "Battery voltage"},
		{"battery.voltage.nominal", UnitVolts, true, "Nominal battery voltage"},
		{"battery.temperature", UnitCelsius, true, "Battery temperature"},
		{"battery.type", "", false, "Battery chemistry"},
		{"battery.date", "", false, "Battery installation or replacement date"},
		{"battery.mfr.date", "", false, "Battery manufacture date"},
		{"input.voltage", UnitVolts, true, "Input (mains) voltage"},
		{"input.voltage.nominal", UnitVolts, true, "Nominal input voltage"},
		{"input.frequency", UnitHertz, true, "Input frequency"},
		{"input.frequency.nominal", UnitHertz, true, "Nominal input frequency"},
		{"input.transfer.low", UnitVolts, true, "Input voltage below which the UPS switches to battery"},
		{"input.transfer.high", UnitVolts, true, "Input voltage above which the UPS switches to battery"},
		{"input.bypass.voltage", UnitVolts, true, "Bypass input voltage"},
		{"input.bypass.frequency", UnitHertz, true, "Bypass input frequency"},
		{"output.voltage", UnitVolts, true, "Output voltage"},
		{"output.voltage.nominal", UnitVolts, true, "Nominal output voltage"},
		{"output.frequency", UnitHertz, true, "Output frequency"},
		{"output.current", UnitAmps, true, "Output current"},
		{"driver.name", "", false, "Name of the driver reading this UPS"},
		{"driver.version", "", false, "Version of the driver reading this UPS"},
	} {
		knownVars[v.Name] = v
	}
}

// LookupVar returns the spec of a well-known variable.
func LookupVar(name string) (VarSpec, bool) {
	v, ok := knownVars[name]
	return v, ok
}

// KnownVars returns all well-known variables sorted by name.
func KnownVars() []VarSpec {
	out := make([]VarSpec, 0, len(knownVars))
	for _, v := range knownVars {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
