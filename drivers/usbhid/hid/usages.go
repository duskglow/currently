package hid

import "fmt"

// Usage pages used by UPSes.
const (
	PagePowerDevice   = 0x84
	PageBatterySystem = 0x85
)

// Usage combines a usage page (high 16 bits) and a usage ID (low 16 bits).
type Usage uint32

// NewUsage returns the usage for a page and ID.
func NewUsage(page, id uint16) Usage { return Usage(uint32(page)<<16 | uint32(id)) }

// Page returns the usage page.
func (u Usage) Page() uint16 { return uint16(u >> 16) }

// ID returns the usage ID within its page.
func (u Usage) ID() uint16 { return uint16(u) }

// Name returns the usage name from the HID Power Device and Battery System
// usage tables, or the usage as 8 hex digits ("ff860016") when unknown.
func (u Usage) Name() string {
	if n, ok := usageNames[u]; ok {
		return n
	}
	return fmt.Sprintf("%08x", uint32(u))
}

// Well-known usages.
var (
	UsageUPS         = NewUsage(PagePowerDevice, 0x04)
	UsagePowerSupply = NewUsage(PagePowerDevice, 0x05)
)

// usageNames holds the names from "USB Usage Tables for HID Power Devices"
// release 1.0, pages 0x84 and 0x85. The names follow NUT's spelling, so
// paths such as UPS.PowerSummary.RemainingCapacity match NUT's.
var usageNames = map[Usage]string{}

func init() {
	power := map[uint16]string{
		0x01: "iName", 0x02: "PresentStatus", 0x03: "ChangedStatus",
		0x04: "UPS", 0x05: "PowerSupply",
		0x10: "BatterySystem", 0x11: "BatterySystemID", 0x12: "Battery", 0x13: "BatteryID",
		0x14: "Charger", 0x15: "ChargerID", 0x16: "PowerConverter", 0x17: "PowerConverterID",
		0x18: "OutletSystem", 0x19: "OutletSystemID", 0x1a: "Input", 0x1b: "InputID",
		0x1c: "Output", 0x1d: "OutputID", 0x1e: "Flow", 0x1f: "FlowID",
		0x20: "Outlet", 0x21: "OutletID", 0x22: "Gang", 0x23: "GangID",
		0x24: "PowerSummary", 0x25: "PowerSummaryID",
		0x30: "Voltage", 0x31: "Current", 0x32: "Frequency", 0x33: "ApparentPower",
		0x34: "ActivePower", 0x35: "PercentLoad", 0x36: "Temperature", 0x37: "Humidity",
		0x38: "BadCount",
		0x40: "ConfigVoltage", 0x41: "ConfigCurrent", 0x42: "ConfigFrequency",
		0x43: "ConfigApparentPower", 0x44: "ConfigActivePower", 0x45: "ConfigPercentLoad",
		0x46: "ConfigTemperature", 0x47: "ConfigHumidity",
		0x50: "SwitchOnControl", 0x51: "SwitchOffControl", 0x52: "ToggleControl",
		0x53: "LowVoltageTransfer", 0x54: "HighVoltageTransfer",
		0x55: "DelayBeforeReboot", 0x56: "DelayBeforeStartup", 0x57: "DelayBeforeShutdown",
		0x58: "Test", 0x59: "ModuleReset", 0x5a: "AudibleAlarmControl",
		0x60: "Present", 0x61: "Good", 0x62: "InternalFailure", 0x63: "VoltageOutOfRange",
		0x64: "FrequencyOutOfRange", 0x65: "Overload", 0x66: "OverCharged",
		0x67: "OverTemperature", 0x68: "ShutdownRequested", 0x69: "ShutdownImminent",
		0x6b: "SwitchOn/Off", 0x6c: "Switchable", 0x6d: "Used", 0x6e: "Boost", 0x6f: "Buck",
		0x70: "Initialized", 0x71: "Tested", 0x72: "AwaitingPower", 0x73: "CommunicationLost",
		0xfd: "iManufacturer", 0xfe: "iProduct", 0xff: "iSerialNumber",
	}
	battery := map[uint16]string{
		0x01: "SMBBatteryMode", 0x02: "SMBBatteryStatus", 0x03: "SMBAlarmWarning",
		0x04: "SMBChargerMode", 0x05: "SMBChargerStatus", 0x06: "SMBChargerSpecInfo",
		0x07: "SMBSelectorState", 0x08: "SMBSelectorPresets", 0x09: "SMBSelectorInfo",
		0x10: "OptionalMfgFunction1", 0x11: "OptionalMfgFunction2", 0x12: "OptionalMfgFunction3",
		0x13: "OptionalMfgFunction4", 0x14: "OptionalMfgFunction5",
		0x15: "ConnectionToSMBus", 0x16: "OutputConnection", 0x17: "ChargerConnection",
		0x18: "BatteryInsertion", 0x19: "Usenext", 0x1a: "OKToUse", 0x1b: "BatterySupported",
		0x1c: "SelectorRevision", 0x1d: "ChargingIndicator",
		0x28: "ManufacturerAccess", 0x29: "RemainingCapacityLimit", 0x2a: "RemainingTimeLimit",
		0x2b: "AtRate", 0x2c: "CapacityMode", 0x2d: "BroadcastToCharger",
		0x2e: "PrimaryBattery", 0x2f: "ChargeController",
		0x40: "TerminateCharge", 0x41: "TerminateDischarge", 0x42: "BelowRemainingCapacityLimit",
		0x43: "RemainingTimeLimitExpired", 0x44: "Charging", 0x45: "Discharging",
		0x46: "FullyCharged", 0x47: "FullyDischarged", 0x48: "ConditioningFlag",
		0x49: "AtRateOK", 0x4a: "SMBErrorCode", 0x4b: "NeedReplacement",
		0x60: "AtRateTimeToFull", 0x61: "AtRateTimeToEmpty", 0x62: "AverageCurrent",
		0x63: "Maxerror", 0x64: "RelativeStateOfCharge", 0x65: "AbsoluteStateOfCharge",
		0x66: "RemainingCapacity", 0x67: "FullChargeCapacity", 0x68: "RunTimeToEmpty",
		0x69: "AverageTimeToEmpty", 0x6a: "AverageTimeToFull", 0x6b: "CycleCount",
		0x80: "BattPackModelLevel", 0x81: "InternalChargeController",
		0x82: "PrimaryBatterySupport", 0x83: "DesignCapacity", 0x84: "SpecificationInfo",
		0x85: "ManufacturerDate", 0x86: "SerialNumber", 0x87: "iManufacturerName",
		0x88: "iDevicename", 0x89: "iDeviceChemistry", 0x8a: "ManufacturerData",
		0x8b: "Rechargeable", 0x8c: "WarningCapacityLimit", 0x8d: "CapacityGranularity1",
		0x8e: "CapacityGranularity2", 0x8f: "iOEMInformation",
		0xc0: "InhibitCharge", 0xc1: "EnablePolling", 0xc2: "ResetToZero",
		0xd0: "ACPresent", 0xd1: "BatteryPresent", 0xd2: "PowerFail", 0xd3: "AlarmInhibited",
		0xd4: "ThermistorUnderRange", 0xd5: "ThermistorHot", 0xd6: "ThermistorCold",
		0xd7: "ThermistorOverRange", 0xd8: "VoltageOutOfRange", 0xd9: "CurrentOutOfRange",
		0xda: "CurrentNotRegulated", 0xdb: "VoltageNotRegulated", 0xdc: "MasterMode",
		0xf0: "ChargerSelectorSupport", 0xf1: "ChargerSpec", 0xf2: "Level2", 0xf3: "Level3",
	}
	for id, n := range power {
		usageNames[NewUsage(PagePowerDevice, id)] = n
	}
	for id, n := range battery {
		usageNames[NewUsage(PageBatterySystem, id)] = n
	}
}

// HID unit codes used by UPSes (SI linear system).
const (
	UnitVolt    uint32 = 0x00F0D121
	UnitWatt    uint32 = 0x0000D121 // also used for VA
	UnitAmpere  uint32 = 0x00100001
	UnitKelvin  uint32 = 0x00010001
	UnitSecond  uint32 = 0x00001001
	UnitHertz   uint32 = 0x0000F001
	UnitAmpSecs uint32 = 0x00101001
)
