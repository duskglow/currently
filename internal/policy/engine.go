// Package policy decides when to shut the computer down and carries the
// shutdown out, following NUT's upsmon semantics: a UPS is critical when
// it is on battery and low (or a configured trigger fires), when FSD is
// set, or when it stops reporting while on battery; the computer shuts
// down when fewer than MinSupplies UPSes are healthy.
package policy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// Supply is a UPS that powers this computer.
type Supply struct {
	Name string `json:"name"`
	// Remote supplies are read from another server (nut driver): this
	// computer is their secondary. Others are attached here: this computer
	// is their primary, and tells NUT clients to shut down via FSD.
	Remote bool `json:"remote"`
	// Simulated supplies (simulator and replay drivers) never trigger the
	// built-in OS shutdown.
	Simulated bool `json:"simulated"`
}

// Config is the shutdown policy.
type Config struct {
	Supplies           []Supply
	MinSupplies        int
	LowBattery         bool
	OnBatteryFor       time.Duration
	RuntimeBelow       time.Duration
	ChargeBelow        float64
	DeadTime           time.Duration
	WaitForSecondaries time.Duration
	FinalDelay         time.Duration
	Hooks              []string
	HookTimeout        time.Duration
	Command            string
	PowerOffUPS        bool
	UPSOffDelay        time.Duration
	DryRun             bool
}

// Host is what the engine needs from the core.
type Host interface {
	GetUPS(name string) (sdk.UPS, bool)
	SetForcedShutdown(ctx context.Context, ups string) error
	RunCommand(ctx context.Context, ups, command, param string) error
	// StopUPS closes a UPS's driver; for remote UPSes this logs out of the
	// server, so its primary knows this computer is going down.
	StopUPS(ups string)
	Publish(e sdk.Event)
}

// Options wire the engine.
type Options struct {
	Host     Host
	Executor Executor
	Log      *slog.Logger
	// Clients returns the NUT clients logged in to a UPS (may be nil).
	Clients func(ups string) []string
}

// States of the engine.
const (
	StateDisabled     = "disabled"
	StateArmed        = "armed"
	StateShuttingDown = "shutting down"
	StateDone         = "done"
)

// Engine watches the supplies and shuts down when needed.
type Engine struct {
	cfg  Config
	opts Options
	now  func() time.Time
	// sleep waits, or returns early when ctx ends; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error

	mu         sync.Mutex
	state      string
	reason     string
	obSince    map[string]time.Time // first seen on battery
	lostSince  map[string]time.Time // first seen not connected
	announced  map[string]bool      // "shutdown scheduled" event sent
	steps      []Step
	rehearsal  bool // the last run did not really shut down
	assessment []SupplyStatus
}

// Step is one step of a shutdown, as carried out.
type Step struct {
	Time   time.Time `json:"time"`
	Action string    `json:"action"`
	Result string    `json:"result"`
}

// New returns an engine. A nil config gives a disabled engine.
func New(cfg *Config, opts Options) *Engine {
	e := &Engine{opts: opts, now: time.Now, sleep: sleepCtx, state: StateDisabled,
		obSince: map[string]time.Time{}, lostSince: map[string]time.Time{}, announced: map[string]bool{}}
	if opts.Log == nil {
		e.opts.Log = slog.Default()
	}
	if cfg != nil {
		e.cfg = *cfg
		e.state = StateArmed
	}
	return e
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run evaluates the policy every second until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	if e.state == StateDisabled {
		return nil
	}
	e.opts.Log.Info("shutdown policy armed", "supplies", e.supplyNames(), "min_supplies", e.cfg.MinSupplies,
		"triggers", strings.Join(e.Triggers(), "; "), "dry_run", e.cfg.DryRun)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		e.Evaluate(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (e *Engine) supplyNames() string {
	var names []string
	for _, s := range e.cfg.Supplies {
		names = append(names, s.Name)
	}
	return strings.Join(names, ",")
}

// SupplyStatus is the assessment of one supply.
type SupplyStatus struct {
	Supply
	Critical bool   `json:"critical"`
	Reason   string `json:"reason,omitempty"`
	// ShutdownIn counts down the on_battery_for trigger, in seconds.
	ShutdownIn *float64 `json:"shutdown_in,omitempty"`
}

// Evaluate assesses the supplies once and starts the shutdown when too few
// are healthy. It returns the reason when it started one.
func (e *Engine) Evaluate(ctx context.Context) string {
	e.mu.Lock()
	if e.state == StateDisabled || e.state == StateShuttingDown {
		e.mu.Unlock()
		return ""
	}
	now := e.now()
	var list []SupplyStatus
	var reasons []string
	healthy := 0
	for _, s := range e.cfg.Supplies {
		st := e.assess(s, now)
		list = append(list, st)
		if st.Critical {
			reasons = append(reasons, s.Name+": "+st.Reason)
		} else {
			healthy++
		}
	}
	e.assessment = list

	if e.state == StateDone {
		// Only a rehearsal re-arms, once every supply is healthy again.
		if e.rehearsal && healthy == len(list) {
			e.state, e.reason = StateArmed, ""
			e.mu.Unlock()
			e.event("", "Shutdown policy re-armed after the rehearsal")
			return ""
		}
		e.mu.Unlock()
		return ""
	}
	if healthy >= e.cfg.MinSupplies {
		e.mu.Unlock()
		return ""
	}
	reason := strings.Join(reasons, "; ")
	e.state, e.reason, e.steps = StateShuttingDown, reason, nil
	var critical []Supply
	for _, st := range list {
		if st.Critical {
			critical = append(critical, st.Supply)
		}
	}
	e.mu.Unlock()

	go e.shutdown(context.WithoutCancel(ctx), reason, critical)
	return reason
}

// assess decides whether one supply is critical. Called with e.mu held.
func (e *Engine) assess(s Supply, now time.Time) SupplyStatus {
	st := SupplyStatus{Supply: s}
	u, ok := e.opts.Host.GetUPS(s.Name)
	if !ok || u.LastUpdate.IsZero() {
		// Never heard from: not counted as critical (as upsmon does), so
		// a slow start never shuts the computer down.
		return st
	}
	status := u.Status()
	if u.State != sdk.StateConnected {
		if _, seen := e.lostSince[s.Name]; !seen {
			e.lostSince[s.Name] = now
		}
		lost := now.Sub(e.lostSince[s.Name])
		switch {
		case status.Has(sdk.FlagForcedShutdown):
			st.Critical, st.Reason = true, "forced shutdown (FSD) was set before contact was lost"
		case status.Has(sdk.FlagOnBattery) && lost >= e.cfg.DeadTime:
			st.Critical, st.Reason = true, fmt.Sprintf("no contact for %s while on battery", lost.Round(time.Second))
		}
		return st
	}
	delete(e.lostSince, s.Name)

	onBattery := status.Has(sdk.FlagOnBattery)
	if onBattery {
		if _, seen := e.obSince[s.Name]; !seen {
			e.obSince[s.Name] = now
			if e.cfg.OnBatteryFor > 0 && !e.announced[s.Name] {
				e.announced[s.Name] = true
				go e.event(s.Name, fmt.Sprintf("UPS %s is on battery: this computer shuts down in %s unless power returns",
					s.Name, e.cfg.OnBatteryFor))
			}
		}
	} else {
		if e.announced[s.Name] {
			go e.event(s.Name, fmt.Sprintf("Power returned to UPS %s; shutdown cancelled", s.Name))
		}
		delete(e.obSince, s.Name)
		delete(e.announced, s.Name)
	}

	switch {
	case status.Has(sdk.FlagForcedShutdown):
		st.Critical, st.Reason = true, "forced shutdown (FSD)"
	case !onBattery:
	case e.cfg.LowBattery && status.Has(sdk.FlagLowBattery):
		st.Critical, st.Reason = true, "on battery and the battery is low"
	case e.cfg.OnBatteryFor > 0 && now.Sub(e.obSince[s.Name]) >= e.cfg.OnBatteryFor:
		st.Critical, st.Reason = true, fmt.Sprintf("on battery for %s", e.cfg.OnBatteryFor)
	case e.cfg.RuntimeBelow > 0 && below(u, "battery.runtime", e.cfg.RuntimeBelow.Seconds()):
		st.Critical, st.Reason = true, fmt.Sprintf("on battery with %ss runtime left (limit %s)", u.Value("battery.runtime"), e.cfg.RuntimeBelow)
	case e.cfg.ChargeBelow > 0 && below(u, "battery.charge", e.cfg.ChargeBelow):
		st.Critical, st.Reason = true, fmt.Sprintf("on battery with %s%% charge (limit %g%%)", u.Value("battery.charge"), e.cfg.ChargeBelow)
	case e.cfg.OnBatteryFor > 0:
		left := (e.cfg.OnBatteryFor - now.Sub(e.obSince[s.Name])).Seconds()
		st.ShutdownIn = &left
	}
	return st
}

func below(u sdk.UPS, name string, limit float64) bool {
	v, ok := u.Float(name)
	return ok && v < limit
}

func (e *Engine) event(ups, msg string) {
	e.opts.Log.Warn(msg, "ups", ups)
	if e.opts.Host != nil {
		e.opts.Host.Publish(sdk.Event{UPS: ups, Type: sdk.EventShutdown, Message: msg})
	}
}

// step records and announces one step.
func (e *Engine) step(action, result string) {
	e.mu.Lock()
	e.steps = append(e.steps, Step{Time: e.now(), Action: action, Result: result})
	e.mu.Unlock()
	msg := action
	if result != "done" {
		msg += " (" + result + ")"
	}
	e.event("", "Shutdown: "+msg)
}

// shutdown carries out the shutdown sequence.
//
// Safety: in a dry run every step is skipped. A rehearsal (every critical
// supply is simulated and no custom command is set) acts only on simulated
// UPSes: it sets no FSD on real UPSes, keeps watching real remote UPSes,
// runs no hooks and does not shut this computer down.
func (e *Engine) shutdown(ctx context.Context, reason string, critical []Supply) {
	dry := e.cfg.DryRun
	simulated := len(critical) > 0
	for _, s := range critical {
		simulated = simulated && s.Simulated
	}
	rehearsal := !dry && simulated && e.cfg.Command == ""
	note := ""
	switch {
	case dry:
		note = " (dry run: nothing will be done)"
	case rehearsal:
		note = " (rehearsal triggered by a simulated UPS: only simulated UPSes are affected)"
	}
	e.event("", "Shutting down this computer: "+reason+note)

	// allowed reports whether to perform an action, recording it as
	// skipped when not. ups is "" for actions on this computer.
	allowed := func(action, ups string) bool {
		switch {
		case dry:
			e.step(action, "skipped: dry run")
			return false
		case rehearsal && (ups == "" || !e.isSimulated(ups)):
			e.step(action, "skipped: rehearsal")
			return false
		}
		return true
	}

	var local, remote []string
	for _, s := range e.cfg.Supplies {
		if s.Remote {
			remote = append(remote, s.Name)
		} else {
			local = append(local, s.Name)
		}
	}

	// 1. As primary, tell NUT clients (upsmon secondaries, other nutng
	// instances) to shut down.
	var fsd []string
	for _, u := range local {
		if allowed("Set FSD on "+u+", telling its NUT clients to shut down", u) {
			e.step("Set FSD on "+u+", telling its NUT clients to shut down", result(e.opts.Host.SetForcedShutdown(ctx, u)))
			fsd = append(fsd, u)
		}
	}

	// 2. Wait for them to log out, as NUT's HOSTSYNC.
	if len(fsd) > 0 && e.opts.Clients != nil {
		action := fmt.Sprintf("Wait up to %s for NUT clients to log out", e.cfg.WaitForSecondaries)
		deadline := e.now().Add(e.cfg.WaitForSecondaries)
		for {
			left := e.clientList(fsd)
			if len(left) == 0 {
				e.step(action, "done")
				break
			}
			if !e.now().Before(deadline) {
				e.step(action, "timed out; still logged in: "+strings.Join(left, ", "))
				break
			}
			if e.sleep(ctx, 500*time.Millisecond) != nil {
				return
			}
		}
	}

	// 3. As secondary, log out of the remote servers.
	for _, u := range remote {
		action := "Log out of the server for " + u
		if allowed(action, u) {
			e.opts.Host.StopUPS(u)
			e.step(action, "done")
		}
	}

	// 4. Hooks.
	env := []string{"NUTNG_SHUTDOWN_REASON=" + reason}
	for _, h := range e.cfg.Hooks {
		action := "Run hook: " + h
		if !allowed(action, "") {
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, e.cfg.HookTimeout)
		err := e.opts.Executor.RunHook(hctx, h, env)
		if errors.Is(hctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("timed out after %s", e.cfg.HookTimeout)
		}
		cancel()
		e.step(action, result(err))
	}

	// 5. Have local UPSes cut power after a delay and restore it when mains
	// returns, so this computer boots again by itself.
	if e.cfg.PowerOffUPS {
		secs := strconv.Itoa(int(e.cfg.UPSOffDelay.Seconds()))
		for _, u := range local {
			action := fmt.Sprintf("Tell UPS %s to cut power in %ss and restore it when mains returns", u, secs)
			if allowed(action, u) {
				e.step(action, result(e.opts.Host.RunCommand(ctx, u, "shutdown.return", secs)))
			}
		}
	}

	// 6. Final delay, then the shutdown command.
	action := "Shut down this computer: " + e.describeCommand()
	if allowed(action, "") {
		if e.cfg.FinalDelay > 0 {
			_ = e.sleep(ctx, e.cfg.FinalDelay)
		}
		e.step(action, result(e.opts.Executor.Shutdown(ctx, e.cfg.Command)))
	}

	e.mu.Lock()
	e.state, e.rehearsal = StateDone, dry || rehearsal
	e.mu.Unlock()
	if dry || rehearsal {
		e.event("", "Shutdown rehearsal finished; the policy re-arms when every UPS is healthy again")
	}
}

func (e *Engine) isSimulated(name string) bool {
	for _, s := range e.cfg.Supplies {
		if s.Name == name {
			return s.Simulated
		}
	}
	return false
}

func (e *Engine) clientList(ups []string) []string {
	var out []string
	for _, u := range ups {
		for _, c := range e.opts.Clients(u) {
			out = append(out, c+" ("+u+")")
		}
	}
	return out
}

func result(err error) string {
	if err != nil {
		return "failed: " + err.Error()
	}
	return "done"
}

func (e *Engine) describeCommand() string {
	if e.cfg.Command != "" {
		return e.cfg.Command
	}
	return strings.Join(DefaultCommand(), " ")
}

// Triggers describes what makes an on-battery UPS critical, completing
// "a UPS on battery is critical when ...".
func (e *Engine) Triggers() []string {
	var t []string
	if e.cfg.LowBattery {
		t = append(t, "its battery is low (the UPS's own signal)")
	}
	if e.cfg.OnBatteryFor > 0 {
		t = append(t, "it has been on battery for "+e.cfg.OnBatteryFor.String())
	}
	if e.cfg.RuntimeBelow > 0 {
		t = append(t, "its runtime is below "+e.cfg.RuntimeBelow.String())
	}
	if e.cfg.ChargeBelow > 0 {
		t = append(t, fmt.Sprintf("its charge is below %g%%", e.cfg.ChargeBelow))
	}
	return t
}

// Plan describes what a shutdown would do now.
func (e *Engine) Plan() []string {
	var p, local []string
	for _, s := range e.cfg.Supplies {
		if s.Remote {
			continue
		}
		local = append(local, s.Name)
		line := "Set FSD on " + s.Name + ", telling its NUT clients to shut down"
		if e.opts.Clients != nil {
			if c := e.opts.Clients(s.Name); len(c) > 0 {
				line += " (logged in now: " + strings.Join(c, ", ") + ")"
			} else {
				line += " (none logged in now)"
			}
		}
		p = append(p, line)
	}
	if len(local) > 0 {
		p = append(p, fmt.Sprintf("Wait up to %s for NUT clients to log out", e.cfg.WaitForSecondaries))
	}
	for _, s := range e.cfg.Supplies {
		if s.Remote {
			p = append(p, "Log out of the server for "+s.Name+", so its primary knows this computer is going down")
		}
	}
	for _, h := range e.cfg.Hooks {
		p = append(p, fmt.Sprintf("Run hook: %s (timeout %s)", h, e.cfg.HookTimeout))
	}
	if e.cfg.PowerOffUPS {
		for _, u := range local {
			p = append(p, fmt.Sprintf("Tell UPS %s to cut power in %s and restore it when mains returns", u, e.cfg.UPSOffDelay))
		}
	}
	if e.cfg.FinalDelay > 0 {
		p = append(p, "Wait "+e.cfg.FinalDelay.String())
	}
	p = append(p, "Shut down this computer: "+e.describeCommand())
	return p
}

// Status is the engine's state, for the API and CLI.
type Status struct {
	Enabled     bool           `json:"enabled"`
	DryRun      bool           `json:"dry_run"`
	State       string         `json:"state"`
	Reason      string         `json:"reason,omitempty"`
	MinSupplies int            `json:"min_supplies"`
	Triggers    []string       `json:"triggers,omitempty"`
	Supplies    []SupplyStatus `json:"supplies,omitempty"`
	Plan        []string       `json:"plan,omitempty"`
	Steps       []Step         `json:"steps,omitempty"`
	// Blocked explains why the built-in OS shutdown would be refused.
	Blocked string `json:"blocked,omitempty"`
}

// Status returns the current state.
func (e *Engine) Status() Status {
	if e.state == StateDisabled {
		return Status{State: StateDisabled}
	}
	plan := e.Plan()
	e.mu.Lock()
	defer e.mu.Unlock()
	st := Status{
		Enabled: true, DryRun: e.cfg.DryRun, State: e.state, Reason: e.reason,
		MinSupplies: e.cfg.MinSupplies, Triggers: e.Triggers(),
		Supplies: slices.Clone(e.assessment), Steps: slices.Clone(e.steps), Plan: plan,
	}
	if st.Supplies == nil {
		for _, s := range e.cfg.Supplies {
			st.Supplies = append(st.Supplies, SupplyStatus{Supply: s})
		}
	}
	if e.cfg.Command == "" {
		st.Blocked = BuiltinBlocked()
	}
	return st
}
