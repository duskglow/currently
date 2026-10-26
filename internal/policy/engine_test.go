package policy

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/duskglow/currently/pkg/sdk"
)

// The tests use only fakes: fakeExec records commands and never runs them.

type fakeHost struct {
	mu       sync.Mutex
	ups      map[string]sdk.UPS
	fsd      []string
	commands []string
	stopped  []string
	events   []sdk.Event
}

func newHost() *fakeHost { return &fakeHost{ups: map[string]sdk.UPS{}} }

func (h *fakeHost) set(name string, state sdk.ConnState, vars ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := map[string]sdk.Variable{}
	for i := 0; i+1 < len(vars); i += 2 {
		m[vars[i]] = sdk.Variable{Name: vars[i], Value: vars[i+1]}
	}
	h.ups[name] = sdk.UPS{Name: name, State: state, Vars: m, LastUpdate: time.Unix(1, 0)}
}

func (h *fakeHost) GetUPS(name string) (sdk.UPS, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	u, ok := h.ups[name]
	return u, ok
}

func (h *fakeHost) SetForcedShutdown(_ context.Context, ups string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fsd = append(h.fsd, ups)
	return nil
}

func (h *fakeHost) RunCommand(_ context.Context, ups, cmd, param string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commands = append(h.commands, ups+" "+cmd+" "+param)
	return nil
}

func (h *fakeHost) StopUPS(ups string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = append(h.stopped, ups)
}

func (h *fakeHost) Publish(e sdk.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
}

func (h *fakeHost) messages() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.events {
		out = append(out, e.Message)
	}
	return strings.Join(out, "\n")
}

type fakeExec struct {
	mu        sync.Mutex
	hooks     []string
	shutdowns []string
	hookDelay time.Duration
}

func (x *fakeExec) RunHook(ctx context.Context, command string, _ []string) error {
	if x.hookDelay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(x.hookDelay):
		}
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.hooks = append(x.hooks, command)
	return nil
}

func (x *fakeExec) Shutdown(_ context.Context, command string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.shutdowns = append(x.shutdowns, "shutdown:"+command)
	return nil
}

func (x *fakeExec) calls() (hooks, shutdowns []string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]string(nil), x.hooks...), append([]string(nil), x.shutdowns...)
}

type harness struct {
	t       *testing.T
	host    *fakeHost
	exec    *fakeExec
	e       *Engine
	mu      sync.Mutex
	now     time.Time
	clients map[string][]string
	// logoutAt, when set, is when every client logs out.
	logoutAt time.Time
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	h := &harness{t: t, host: newHost(), exec: &fakeExec{}, now: time.Unix(1000, 0), clients: map[string][]string{}}
	if cfg.MinSupplies == 0 {
		cfg.MinSupplies = 1
	}
	if cfg.DeadTime == 0 {
		cfg.DeadTime = 15 * time.Second
	}
	if cfg.HookTimeout == 0 {
		cfg.HookTimeout = time.Minute
	}
	h.e = New(&cfg, Options{Host: h.host, Executor: h.exec,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clients: func(ups string) []string {
			h.mu.Lock()
			defer h.mu.Unlock()
			if !h.logoutAt.IsZero() && !h.now.Before(h.logoutAt) {
				return nil
			}
			return h.clients[ups]
		}})
	h.e.now = func() time.Time {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.now
	}
	h.e.sleep = func(_ context.Context, d time.Duration) error { h.advance(d); return nil }
	return h
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

// evaluate runs one evaluation and waits for any shutdown it started.
func (h *harness) evaluate() string {
	h.t.Helper()
	reason := h.e.Evaluate(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for h.e.Status().State == StateShuttingDown {
		if time.Now().After(deadline) {
			h.t.Fatal("shutdown did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	return reason
}

func (h *harness) actions() string {
	var out []string
	for _, s := range h.e.Status().Steps {
		out = append(out, s.Action+" => "+s.Result)
	}
	return strings.Join(out, "\n")
}

const (
	onLine     = "OL"
	onBattery  = "OB DISCHRG"
	lowBattery = "OB DISCHRG LB"
)

func TestNoShutdownWhileHealthyOrUnknown(t *testing.T) {
	h := newHarness(t, Config{Supplies: []Supply{{Name: "rack"}}, LowBattery: true, Command: "custom"})
	if r := h.evaluate(); r != "" {
		t.Fatalf("never-seen UPS triggered a shutdown: %s", r)
	}
	h.host.set("rack", sdk.StateConnecting) // no data yet
	h.host.mu.Lock()
	u := h.host.ups["rack"]
	u.LastUpdate = time.Time{}
	h.host.ups["rack"] = u
	h.host.mu.Unlock()
	if r := h.evaluate(); r != "" {
		t.Fatalf("connecting UPS triggered a shutdown: %s", r)
	}
	h.host.set("rack", sdk.StateConnected, "ups.status", onLine)
	if r := h.evaluate(); r != "" {
		t.Fatalf("online UPS triggered a shutdown: %s", r)
	}
	h.host.set("rack", sdk.StateConnected, "ups.status", onBattery, "battery.charge", "90")
	if r := h.evaluate(); r != "" {
		t.Fatalf("on battery without a trigger: %s", r)
	}
	if _, shutdowns := h.exec.calls(); len(shutdowns) != 0 {
		t.Fatalf("shutdown called: %v", shutdowns)
	}
}

func TestPrimarySequence(t *testing.T) {
	h := newHarness(t, Config{Supplies: []Supply{{Name: "rack"}}, LowBattery: true,
		WaitForSecondaries: 10 * time.Second, FinalDelay: 5 * time.Second,
		Hooks: []string{"stop-vms"}, Command: "custom-poweroff"})
	h.clients["rack"] = []string{"10.0.0.5"}
	h.logoutAt = time.Unix(1002, 0) // the secondary logs out two seconds after FSD
	h.host.set("rack", sdk.StateConnected, "ups.status", lowBattery)
	if r := h.evaluate(); !strings.Contains(r, "battery is low") {
		t.Fatalf("reason = %q", r)
	}
	want := []string{
		"Set FSD on rack, telling its NUT clients to shut down => done",
		"Wait up to 10s for NUT clients to log out => done",
		"Run hook: stop-vms => done",
		"Shut down this computer: custom-poweroff => done",
	}
	if got := h.actions(); got != strings.Join(want, "\n") {
		t.Errorf("steps:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	hooks, shutdowns := h.exec.calls()
	if len(hooks) != 1 || len(shutdowns) != 1 || shutdowns[0] != "shutdown:custom-poweroff" {
		t.Errorf("hooks %v, shutdowns %v", hooks, shutdowns)
	}
	if len(h.host.fsd) != 1 {
		t.Errorf("FSD set %v", h.host.fsd)
	}
	// A real shutdown never re-arms.
	h.host.set("rack", sdk.StateConnected, "ups.status", onLine)
	h.evaluate()
	if st := h.e.Status().State; st != StateDone {
		t.Errorf("state after a real shutdown = %s", st)
	}
}

func TestWaitForSecondariesTimesOut(t *testing.T) {
	h := newHarness(t, Config{Supplies: []Supply{{Name: "rack"}}, LowBattery: true,
		WaitForSecondaries: 3 * time.Second, Command: "x"})
	h.clients["rack"] = []string{"10.0.0.9"}
	h.host.set("rack", sdk.StateConnected, "ups.status", lowBattery)
	h.evaluate()
	if got := h.actions(); !strings.Contains(got, "timed out; still logged in: 10.0.0.9 (rack)") {
		t.Errorf("steps:\n%s", got)
	}
	if _, s := h.exec.calls(); len(s) != 1 {
		t.Errorf("must shut down after the wait times out: %v", s)
	}
}

func TestOnBatteryForCountdownAndCancel(t *testing.T) {
	h := newHarness(t, Config{Supplies: []Supply{{Name: "rack"}}, LowBattery: true, OnBatteryFor: 2 * time.Minute, Command: "x"})
	h.host.set("rack", sdk.StateConnected, "ups.status", onBattery)
	h.evaluate()
	h.advance(time.Minute)
	h.evaluate()
	st := h.e.Status()
	if st.State != StateArmed || st.Supplies[0].ShutdownIn == nil || *st.Supplies[0].ShutdownIn != 60 {
		t.Fatalf("after 1m: %+v", st.Supplies[0])
	}
	// Power returns: no shutdown, and the countdown restarts next time.
	h.host.set("rack", sdk.StateConnected, "ups.status", onLine)
	h.evaluate()
	h.host.set("rack", sdk.StateConnected, "ups.status", onBattery)
	h.advance(90 * time.Second)
	if r := h.evaluate(); r != "" {
		t.Fatalf("countdown did not restart: %s", r)
	}
	h.advance(2 * time.Minute)
	if r := h.evaluate(); !strings.Contains(r, "on battery for 2m0s") {
		t.Fatalf("reason = %q", r)
	}
	msgs := h.host.messages()
	for _, want := range []string{"shuts down in 2m0s unless power returns", "shutdown cancelled"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("events lack %q:\n%s", want, msgs)
		}
	}
}

func TestRuntimeAndChargeTriggers(t *testing.T) {
	h := newHarness(t, Config{Supplies: []Supply{{Name: "rack"}}, RuntimeBelow: 5 * time.Minute, Command: "x"})
	h.host.set("rack", sdk.StateConnected, "ups.status", onBattery, "battery.runtime", "600")
	if r := h.evaluate(); r != "" {
		t.Fatalf("10m runtime triggered: %s", r)
	}
	h.host.set("rack", sdk.StateConnected, "ups.status", onBattery, "battery.runtime", "240")
	if r := h.evaluate(); !strings.Contains(r, "240s runtime left") {
		t.Fatalf("reason = %q", r)
	}

	h = newHarness(t, Config{Supplies: []Supply{{Name: "rack"}}, ChargeBelow: 30, Command: "x"})
	h.host.set("rack", sdk.StateConnected, "ups.status", onLine, "battery.charge", "10")
	if r := h.evaluate(); r != "" {
		t.Fatalf("low charge on line power triggered: %s", r)
	}
	h.host.set("rack", sdk.StateConnected, "ups.status", onBattery, "battery.charge", "25")
	if r := h.evaluate(); !strings.Contains(r, "25% charge") {
		t.Fatalf("reason = %q", r)
	}
}

func TestLostContact(t *testing.T) {
	h := newHarness(t, Config{Supplies: []Supply{{Name: "rack"}}, LowBattery: true, DeadTime: 15 * time.Second, Command: "x"})
	h.host.set("rack", sdk.StateStale, "ups.status", onLine)
	h.advance(time.Hour)
	if r := h.evaluate(); r != "" {
		t.Fatalf("lost contact while online triggered: %s", r)
	}
	h.host.set("rack", sdk.StateFailed, "ups.status", onBattery)
	h.evaluate()
	h.advance(10 * time.Second)
	if r := h.evaluate(); r != "" {
		t.Fatalf("triggered before dead time: %s", r)
	}
	h.advance(10 * time.Second)
	if r := h.evaluate(); !strings.Contains(r, "no contact") {
		t.Fatalf("reason = %q", r)
	}
}

func TestMinSupplies(t *testing.T) {
	supplies := []Supply{{Name: "a"}, {Name: "b"}}
	h := newHarness(t, Config{Supplies: supplies, MinSupplies: 1, LowBattery: true, Command: "x"})
	h.host.set("a", sdk.StateConnected, "ups.status", lowBattery)
	h.host.set("b", sdk.StateConnected, "ups.status", onLine)
	if r := h.evaluate(); r != "" {
		t.Fatalf("one healthy supply of the one needed, but shut down: %s", r)
	}
	h = newHarness(t, Config{Supplies: supplies, MinSupplies: 2, LowBattery: true, Command: "x"})
	h.host.set("a", sdk.StateConnected, "ups.status", lowBattery)
	h.host.set("b", sdk.StateConnected, "ups.status", onLine)
	if r := h.evaluate(); !strings.HasPrefix(r, "a: ") {
		t.Fatalf("reason = %q", r)
	}
}

func TestSecondaryFollowsRemoteFSD(t *testing.T) {
	h := newHarness(t, Config{Supplies: []Supply{{Name: "office", Remote: true}}, LowBattery: true,
		Hooks: []string{"h"}, Command: "x"})
	h.host.set("office", sdk.StateConnected, "ups.status", "OL FSD")
	if r := h.evaluate(); !strings.Contains(r, "FSD") {
		t.Fatalf("reason = %q", r)
	}
	if len(h.host.fsd) != 0 {
		t.Errorf("a secondary set FSD: %v", h.host.fsd)
	}
	if len(h.host.stopped) != 1 || h.host.stopped[0] != "office" {
		t.Errorf("did not log out of the server: %v", h.host.stopped)
	}
	if _, s := h.exec.calls(); len(s) != 1 {
		t.Errorf("shutdowns %v", s)
	}
}

func TestDryRunDoesNothing(t *testing.T) {
	h := newHarness(t, Config{Supplies: []Supply{{Name: "rack"}, {Name: "office", Remote: true}}, LowBattery: true,
		Hooks: []string{"h"}, PowerOffUPS: true, UPSOffDelay: time.Minute, DryRun: true})
	h.clients["rack"] = []string{"10.0.0.5"}
	h.host.set("rack", sdk.StateConnected, "ups.status", lowBattery)
	h.host.set("office", sdk.StateConnected, "ups.status", lowBattery)
	h.evaluate()
	hooks, shutdowns := h.exec.calls()
	if len(hooks)+len(shutdowns)+len(h.host.fsd)+len(h.host.commands)+len(h.host.stopped) != 0 {
		t.Fatalf("dry run acted: hooks %v shutdowns %v fsd %v commands %v stopped %v",
			hooks, shutdowns, h.host.fsd, h.host.commands, h.host.stopped)
	}
	for _, s := range h.e.Status().Steps {
		if s.Result != "skipped: dry run" {
			t.Errorf("step %q: %s", s.Action, s.Result)
		}
	}
	// It re-arms once power is back.
	h.host.set("rack", sdk.StateConnected, "ups.status", onLine)
	h.host.set("office", sdk.StateConnected, "ups.status", onLine)
	h.evaluate()
	if st := h.e.Status().State; st != StateArmed {
		t.Errorf("state after the rehearsal = %s", st)
	}
}

func TestSimulatedTriggerOnlyTouchesSimulatedUPSes(t *testing.T) {
	h := newHarness(t, Config{
		Supplies:    []Supply{{Name: "sim", Simulated: true}, {Name: "real"}, {Name: "remote", Remote: true}},
		MinSupplies: 3, LowBattery: true, Hooks: []string{"h"}, PowerOffUPS: true, UPSOffDelay: time.Minute,
	})
	h.host.set("sim", sdk.StateConnected, "ups.status", lowBattery)
	h.host.set("real", sdk.StateConnected, "ups.status", onLine)
	h.host.set("remote", sdk.StateConnected, "ups.status", onLine)
	if r := h.evaluate(); !strings.HasPrefix(r, "sim: ") {
		t.Fatalf("reason = %q", r)
	}
	hooks, shutdowns := h.exec.calls()
	if len(hooks) != 0 || len(shutdowns) != 0 {
		t.Errorf("rehearsal ran hooks %v or shut down %v", hooks, shutdowns)
	}
	if strings.Join(h.host.fsd, ",") != "sim" {
		t.Errorf("FSD set on %v; only the simulated UPS may get it", h.host.fsd)
	}
	if strings.Join(h.host.commands, ",") != "sim shutdown.return 60" {
		t.Errorf("UPS commands %v; only the simulated UPS may be powered off", h.host.commands)
	}
	if len(h.host.stopped) != 0 {
		t.Errorf("logged out of a real server: %v", h.host.stopped)
	}
	if !strings.Contains(h.host.messages(), "rehearsal") {
		t.Error("no rehearsal notice")
	}
}

func TestHookTimeout(t *testing.T) {
	h := newHarness(t, Config{Supplies: []Supply{{Name: "rack"}}, LowBattery: true,
		Hooks: []string{"slow"}, HookTimeout: 20 * time.Millisecond, Command: "x"})
	h.exec.hookDelay = time.Second
	h.host.set("rack", sdk.StateConnected, "ups.status", lowBattery)
	h.evaluate()
	if got := h.actions(); !strings.Contains(got, "Run hook: slow => failed: timed out after 20ms") {
		t.Errorf("steps:\n%s", got)
	}
	if _, s := h.exec.calls(); len(s) != 1 {
		t.Errorf("a failed hook must not stop the shutdown: %v", s)
	}
}

func TestStatusAndPlan(t *testing.T) {
	h := newHarness(t, Config{Supplies: []Supply{{Name: "rack"}, {Name: "office", Remote: true}},
		LowBattery: true, OnBatteryFor: time.Minute, WaitForSecondaries: 15 * time.Second,
		FinalDelay: 5 * time.Second, Hooks: []string{"stop-vms"}, PowerOffUPS: true, UPSOffDelay: time.Minute})
	h.clients["rack"] = []string{"10.0.0.5"}
	st := h.e.Status()
	if !st.Enabled || st.State != StateArmed || len(st.Supplies) != 2 {
		t.Fatalf("status %+v", st)
	}
	if st.Blocked != "running inside a test" {
		t.Errorf("built-in shutdown not blocked in tests: %q", st.Blocked)
	}
	plan := strings.Join(st.Plan, "\n")
	for _, want := range []string{"Set FSD on rack", "logged in now: 10.0.0.5", "Wait up to 15s",
		"Log out of the server for office", "Run hook: stop-vms", "Tell UPS rack to cut power in 1m0s", "Wait 5s",
		"Shut down this computer: " + strings.Join(DefaultCommand(), " ")} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan lacks %q:\n%s", want, plan)
		}
	}
	if got := strings.Join(h.e.Triggers(), "; "); got != "its battery is low (the UPS's own signal); it has been on battery for 1m0s" {
		t.Errorf("triggers %q", got)
	}
	if New(nil, Options{}).Status().State != StateDisabled {
		t.Error("nil config must disable the engine")
	}
}

func TestBuiltinBlockedInTests(t *testing.T) {
	if BuiltinBlocked() == "" {
		t.Fatal("the built-in OS shutdown must be blocked inside tests")
	}
}
