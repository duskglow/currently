package sdk

import "strings"

// Flag is one NUT status flag, as found in the ups.status variable.
type Flag string

const (
	FlagOnline         Flag = "OL"
	FlagOnBattery      Flag = "OB"
	FlagLowBattery     Flag = "LB"
	FlagHighBattery    Flag = "HB"
	FlagReplaceBattery Flag = "RB"
	FlagCharging       Flag = "CHRG"
	FlagDischarging    Flag = "DISCHRG"
	FlagBypass         Flag = "BYPASS"
	FlagCalibrating    Flag = "CAL"
	FlagOff            Flag = "OFF"
	FlagOverload       Flag = "OVER"
	FlagTrim           Flag = "TRIM"
	FlagBoost          Flag = "BOOST"
	FlagForcedShutdown Flag = "FSD"
	FlagAlarm          Flag = "ALARM"
	FlagTest           Flag = "TEST"
)

var flagText = map[Flag]string{
	FlagOnline:         "online",
	FlagOnBattery:      "on battery",
	FlagLowBattery:     "low battery",
	FlagHighBattery:    "high battery",
	FlagReplaceBattery: "replace battery",
	FlagCharging:       "charging",
	FlagDischarging:    "discharging",
	FlagBypass:         "on bypass",
	FlagCalibrating:    "calibrating",
	FlagOff:            "output off",
	FlagOverload:       "overloaded",
	FlagTrim:           "trimming voltage",
	FlagBoost:          "boosting voltage",
	FlagForcedShutdown: "forced shutdown",
	FlagAlarm:          "alarm",
	FlagTest:           "testing",
}

// Description returns a short human-readable text for the flag.
func (f Flag) Description() string {
	if s, ok := flagText[f]; ok {
		return s
	}
	return strings.ToLower(string(f))
}

// Status is the parsed form of ups.status.
type Status []Flag

// ParseStatus splits a ups.status value such as "OL CHRG" into flags.
func ParseStatus(s string) Status {
	fields := strings.Fields(s)
	st := make(Status, 0, len(fields))
	for _, f := range fields {
		st = append(st, Flag(strings.ToUpper(f)))
	}
	return st
}

// Has reports whether the status contains flag f.
func (s Status) Has(f Flag) bool {
	for _, x := range s {
		if x == f {
			return true
		}
	}
	return false
}

// String returns the NUT form, for example "OL CHRG".
func (s Status) String() string {
	parts := make([]string, len(s))
	for i, f := range s {
		parts[i] = string(f)
	}
	return strings.Join(parts, " ")
}

// Describe returns a human-readable form, for example "online, charging".
func (s Status) Describe() string {
	if len(s) == 0 {
		return "unknown"
	}
	parts := make([]string, len(s))
	for i, f := range s {
		parts[i] = f.Description()
	}
	return strings.Join(parts, ", ")
}
