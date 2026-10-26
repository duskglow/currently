package simulator

import (
	"math"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/duskglow/currently/pkg/sdk"
)

// model is a simple UPS: mains power comes and goes according to the
// scenario, the battery drains in proportion to the load while on battery
// and recharges on mains.
type model struct {
	opts Options
	rng  *rand.Rand

	elapsed    time.Duration // simulated time since start
	onBattery  bool
	outputOff  bool
	charge     float64 // percent
	load       float64 // percent
	lowCharge  float64 // percent
	lowRuntime float64 // seconds
	beeper     bool

	manual     *bool         // forced mains state set by sim.power.* commands
	testUntil  time.Duration // battery test runs until this simulated time
	testResult string

	// Power commands (load.off.delay, shutdown.return): the output goes off
	// at offAt; with returnAfter set, it comes back that long after mains
	// power is present.
	offAt       time.Duration // 0: none pending
	cutOff      bool          // output cut by a command
	returnAfter time.Duration // 0: stay off
	backAt      time.Duration // 0: not scheduled
}

const (
	batteryNominal = 24.0
	batteryEmpty   = 21.0
	batteryFull    = 27.3
	powerFactor    = 0.6
	testLength     = 10 * time.Second
)

func newModel(o Options) *model {
	seed := uint64(len(o.Serial))
	for _, c := range o.Serial {
		seed = seed*31 + uint64(c)
	}
	return &model{
		opts:       o,
		rng:        rand.New(rand.NewPCG(seed, 0x6e7574)),
		charge:     o.Charge,
		load:       o.Load,
		lowCharge:  20,
		lowRuntime: 120,
		beeper:     true,
		testResult: "No test initiated",
	}
}

// loadFactor scales drain: the configured runtime applies at 50% load.
func (m *model) loadFactor() float64 { return math.Max(m.load, 5) / 50 }

// runtime is the estimated seconds left on battery at the current load.
func (m *model) runtime() float64 {
	return m.charge / 100 * m.opts.Runtime.Seconds() / m.loadFactor()
}

// mainsUp reports whether the scenario has mains power at the current time.
func (m *model) mainsUp() bool {
	if m.elapsed < m.testUntil {
		return false // a battery test runs the load from the battery
	}
	if m.manual != nil {
		return *m.manual
	}
	switch m.opts.Scenario {
	case ScenarioOnBattery:
		return false
	case ScenarioCycle:
		period := m.opts.OnlineFor + m.opts.OutageFor
		return m.elapsed%period < m.opts.OnlineFor
	default:
		return true
	}
}

// step advances the simulation by dt of simulated time.
func (m *model) step(dt time.Duration) {
	m.elapsed += dt
	if m.testUntil > 0 && m.elapsed >= m.testUntil {
		m.testUntil = 0
		m.testResult = "Done and passed"
	}
	m.onBattery = !m.mainsUp()
	m.stepPower()
	secs := dt.Seconds()
	if m.onBattery {
		if !m.outputOff {
			m.charge -= 100 / (m.opts.Runtime.Seconds() / m.loadFactor()) * secs
		}
		if m.charge <= 0 {
			m.charge = 0
			m.outputOff = true // battery exhausted: the UPS cuts the load
		}
	} else {
		m.outputOff = m.cutOff
		m.charge = math.Min(100, m.charge+100/m.opts.RechargeTime.Seconds()*secs)
	}
	if m.cutOff {
		m.outputOff = true
	}
}

// stepPower carries out pending power commands.
func (m *model) stepPower() {
	if m.offAt > 0 && m.elapsed >= m.offAt {
		m.offAt, m.cutOff = 0, true
	}
	if !m.cutOff || m.returnAfter == 0 {
		return
	}
	switch {
	case m.onBattery:
		m.backAt = 0
	case m.backAt == 0:
		m.backAt = m.elapsed + m.returnAfter
	case m.elapsed >= m.backAt:
		m.cutOff, m.returnAfter, m.backAt = false, 0, 0
	}
}

// timers returns ups.timer.shutdown and ups.timer.start in seconds, or -1.
func (m *model) timers() (shutdown, start float64) {
	shutdown, start = -1, -1
	if m.offAt > 0 {
		shutdown = (m.offAt - m.elapsed).Seconds()
	}
	if m.backAt > 0 {
		start = (m.backAt - m.elapsed).Seconds()
	}
	return
}

func (m *model) status() sdk.Status {
	var st sdk.Status
	if m.onBattery {
		st = append(st, sdk.FlagOnBattery)
	} else {
		st = append(st, sdk.FlagOnline)
	}
	switch {
	case m.outputOff:
		st = append(st, sdk.FlagOff)
	case m.onBattery:
		st = append(st, sdk.FlagDischarging)
	case m.charge < 99.5:
		st = append(st, sdk.FlagCharging)
	}
	if m.onBattery && (m.charge <= m.lowCharge || m.runtime() <= m.lowRuntime) {
		st = append(st, sdk.FlagLowBattery)
	}
	if m.elapsed < m.testUntil {
		st = append(st, sdk.FlagTest)
	}
	return st
}

func (m *model) vars() map[string]string {
	o := m.opts
	jitter := func(spread float64) float64 { return (m.rng.Float64()*2 - 1) * spread }

	inV, inF := 0.0, 0.0
	if !m.onBattery {
		inV = o.NominalVoltage + jitter(o.NominalVoltage*0.01)
		inF = o.NominalFrequency + jitter(0.05)
	}
	outV, outF, load := o.NominalVoltage, o.NominalFrequency, m.load
	if m.outputOff {
		outV, outF, load = 0, 0, 0
	}
	battV := batteryEmpty + (batteryFull-batteryEmpty)*m.charge/100
	if m.onBattery {
		battV -= 0.6 // voltage sags under load
	}
	beeper := "enabled"
	if !m.beeper {
		beeper = "disabled"
	}
	result := m.testResult
	if m.elapsed < m.testUntil {
		result = "In progress"
	}
	realNominal := o.PowerNominal * powerFactor
	timerShutdown, timerStart := m.timers()

	return map[string]string{
		"device.mfr":              o.Manufacturer,
		"device.model":            o.Model,
		"device.serial":           o.Serial,
		"device.type":             "ups",
		"ups.mfr":                 o.Manufacturer,
		"ups.model":               o.Model,
		"ups.serial":              o.Serial,
		"ups.firmware":            "sim-1.0",
		"ups.status":              m.status().String(),
		"ups.load":                f0(load),
		"ups.power":               f0(load / 100 * o.PowerNominal),
		"ups.power.nominal":       f0(o.PowerNominal),
		"ups.realpower":           f0(load / 100 * realNominal),
		"ups.realpower.nominal":   f0(realNominal),
		"ups.temperature":         f1(27 + load/20 + jitter(0.2)),
		"ups.beeper.status":       beeper,
		"ups.test.result":         result,
		"ups.delay.shutdown":      "20",
		"ups.delay.start":         "30",
		"ups.timer.shutdown":      f0(timerShutdown),
		"ups.timer.start":         f0(timerStart),
		"battery.charge":          f0(m.charge),
		"battery.charge.low":      f0(m.lowCharge),
		"battery.runtime":         f0(m.runtime()),
		"battery.runtime.low":     f0(m.lowRuntime),
		"battery.voltage":         f1(battV),
		"battery.voltage.nominal": f1(batteryNominal),
		"battery.type":            "PbAc",
		"input.voltage":           f1(inV),
		"input.voltage.nominal":   f0(o.NominalVoltage),
		"input.frequency":         f1(inF),
		"input.frequency.nominal": f0(o.NominalFrequency),
		"input.transfer.low":      f0(o.NominalVoltage * 0.8),
		"input.transfer.high":     f0(o.NominalVoltage * 1.12),
		"output.voltage":          f1(outV),
		"output.voltage.nominal":  f0(o.NominalVoltage),
		"output.frequency":        f1(outF),
		"driver.name":             DriverName,
		"driver.version":          Version,
	}
}

func f0(v float64) string { return strconv.FormatFloat(math.Round(v), 'f', 0, 64) }
func f1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
