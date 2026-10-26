package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nut-ng/nut-ng/pkg/sdk"
)

func types(events []sdk.Event) []sdk.EventType {
	out := make([]sdk.EventType, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

func drain(ch <-chan sdk.Event) []sdk.Event {
	var out []sdk.Event
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestStorePowerEvents(t *testing.T) {
	bus := NewBus()
	s := NewStore(bus)
	s.Add("rack", "", "fake", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.Subscribe(ctx, 100)

	s.Apply("rack", sdk.Update{Vars: map[string]string{"ups.status": "OL", "battery.charge": "100"}})
	if got := types(drain(ch)); len(got) != 1 || got[0] != sdk.EventUpdate {
		t.Fatalf("first update: got %v, want [update]", got)
	}

	s.Apply("rack", sdk.Update{Vars: map[string]string{"ups.status": "OB DISCHRG", "battery.charge": "100"}})
	events := drain(ch)
	if got := types(events); len(got) != 2 || got[1] != sdk.EventOnBattery {
		t.Fatalf("power fail: got %v", got)
	}
	if _, ok := events[0].Changed["battery.charge"]; ok {
		t.Error("unchanged battery.charge reported as changed")
	}

	s.Apply("rack", sdk.Update{Vars: map[string]string{"ups.status": "OB DISCHRG LB"}})
	if got := types(drain(ch)); len(got) != 2 || got[1] != sdk.EventLowBattery {
		t.Fatalf("low battery: got %v", got)
	}

	s.Apply("rack", sdk.Update{Vars: map[string]string{"ups.status": "OL CHRG"}})
	if got := types(drain(ch)); len(got) != 2 || got[1] != sdk.EventOnLine {
		t.Fatalf("power back: got %v", got)
	}

	u, _ := s.Get("rack")
	if u.State != sdk.StateConnected || !u.Status().Has(sdk.FlagCharging) {
		t.Errorf("snapshot = %+v", u)
	}
	if u.Vars["battery.charge"].Unit != sdk.UnitPercent {
		t.Errorf("unit not filled in: %+v", u.Vars["battery.charge"])
	}
	if recent := bus.Recent(10); len(recent) != 3 {
		t.Errorf("Recent kept %d notable events, want 3", len(recent))
	}
}

func TestStoreStale(t *testing.T) {
	bus := NewBus()
	s := NewStore(bus)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	s.Add("rack", "", "fake", 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.Subscribe(ctx, 100)

	s.Apply("rack", sdk.Update{Vars: map[string]string{"ups.status": "OL"}})
	now = now.Add(5 * time.Second)
	s.CheckStale()
	if u, _ := s.Get("rack"); u.State != sdk.StateConnected {
		t.Fatalf("state = %s before timeout", u.State)
	}
	now = now.Add(10 * time.Second)
	s.CheckStale()
	if u, _ := s.Get("rack"); u.State != sdk.StateStale {
		t.Fatalf("state = %s after timeout", u.State)
	}
	s.Apply("rack", sdk.Update{Vars: map[string]string{"ups.status": "OL"}})
	got := types(drain(ch))
	want := []sdk.EventType{sdk.EventUpdate, sdk.EventCommLost, sdk.EventCommRestored}
	if len(got) != len(want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events %v, want %v", got, want)
		}
	}
}

func TestStoreHistory(t *testing.T) {
	s := NewStore(NewBus())
	s.historyLen = 3
	now := time.Unix(0, 0)
	s.now = func() time.Time { return now }
	s.Add("rack", "", "fake", 0)
	for i := 0; i < 5; i++ {
		s.Apply("rack", sdk.Update{Vars: map[string]string{"battery.charge": string(rune('0' + i)), "ups.mfr": "7"}})
		now = now.Add(s.historyInterval)
	}
	h, err := s.History("rack", "battery.charge")
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 3 || h[0].Value != 2 || h[2].Value != 4 {
		t.Errorf("history = %+v", h)
	}
	if _, err := s.History("rack", "ups.mfr"); !errors.Is(err, sdk.ErrNotFound) {
		t.Errorf("non-numeric variable has history: %v", err)
	}
	if _, err := s.History("rakc", "battery.charge"); err == nil || !strings.Contains(err.Error(), `did you mean "rack"`) {
		t.Errorf("want suggestion, got %v", err)
	}
}

func TestBusSlowSubscriberDoesNotBlock(t *testing.T) {
	bus := NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	ch := bus.Subscribe(ctx, 1)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			bus.Publish(sdk.Event{Type: sdk.EventUpdate})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
	cancel()
	for range ch { // must be closed after cancel
	}
}

// fakeDriver opens devices that report one update and then wait, or fail.
type fakeDriver struct {
	opens   atomic.Int32
	failFor int32 // fail this many opens first
}

func (d *fakeDriver) Info() sdk.Info                                     { return sdk.Info{Name: "fake", Kind: sdk.KindDriver} }
func (d *fakeDriver) Discover(context.Context) ([]sdk.Discovered, error) { return nil, nil }
func (d *fakeDriver) ValidateConfig(sdk.Config) error                    { return nil }
func (d *fakeDriver) Open(context.Context, sdk.Config, *slog.Logger) (sdk.Device, error) {
	if n := d.opens.Add(1); n <= d.failFor {
		return nil, errors.New("device busy")
	}
	return &fakeDevice{}, nil
}

type fakeDevice struct {
	mu   sync.Mutex
	ran  []string
	vars map[string]string
}

func (f *fakeDevice) Watch(ctx context.Context, sink func(sdk.Update)) error {
	sink(sdk.Update{Vars: map[string]string{"ups.status": "OL", "battery.charge": "90"}})
	<-ctx.Done()
	return ctx.Err()
}
func (f *fakeDevice) Commands() []sdk.Command { return []sdk.Command{sdk.NewCommand("beeper.disable")} }
func (f *fakeDevice) Writable() []string      { return []string{"battery.charge.low"} }
func (f *fakeDevice) RunCommand(_ context.Context, name, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, name)
	return nil
}
func (f *fakeDevice) SetVar(context.Context, string, string) error { return nil }
func (f *fakeDevice) Close() error                                 { return nil }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCoreRetriesAndCommands(t *testing.T) {
	c := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.MinBackoff, c.MaxBackoff = time.Millisecond, 5*time.Millisecond
	drv := &fakeDriver{failFor: 2}
	if err := c.AddUPS(UPSSpec{Name: "rack", Driver: drv}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddUPS(UPSSpec{Name: "rack", Driver: drv}); err == nil {
		t.Fatal("duplicate UPS accepted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitFor(t, "connection", func() bool {
		u, _ := c.GetUPS("rack")
		return u.State == sdk.StateConnected
	})
	u, _ := c.GetUPS("rack")
	if u.Restarts != 2 {
		t.Errorf("restarts = %d, want 2", u.Restarts)
	}

	if err := c.RunCommand(ctx, "rack", "beeper.disable", ""); err != nil {
		t.Errorf("RunCommand: %v", err)
	}
	if err := c.RunCommand(ctx, "rack", "beeper.disabel", ""); !errors.Is(err, sdk.ErrNotFound) || !strings.Contains(err.Error(), "did you mean") {
		t.Errorf("unknown command: %v", err)
	}
	if err := c.RunCommand(ctx, "nope", "beeper.disable", ""); !errors.Is(err, sdk.ErrNotFound) {
		t.Errorf("unknown UPS: %v", err)
	}
	if err := c.SetVar(ctx, "rack", "ups.status", "OB"); !errors.Is(err, sdk.ErrNotSupported) {
		t.Errorf("read-only variable: %v", err)
	}
	if err := c.SetVar(ctx, "rack", "battery.charge.low", "30"); err != nil {
		t.Errorf("SetVar: %v", err)
	}
}

func TestForcedShutdownPersists(t *testing.T) {
	bus := NewBus()
	s := NewStore(bus)
	s.Add("rack", "", "fake", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.Subscribe(ctx, 100)

	s.Apply("rack", sdk.Update{Vars: map[string]string{"ups.status": "OB LB"}})
	if err := s.SetFSD("rack"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFSD("nope"); !errors.Is(err, sdk.ErrNotFound) {
		t.Errorf("unknown UPS: %v", err)
	}
	u, _ := s.Get("rack")
	if got := u.Value("ups.status"); got != "OB LB FSD" {
		t.Errorf("status after FSD = %q", got)
	}
	// The driver keeps reporting its own status; FSD must stay.
	s.Apply("rack", sdk.Update{Vars: map[string]string{"ups.status": "OB LB"}})
	u, _ = s.Get("rack")
	if got := u.Value("ups.status"); got != "OB LB FSD" {
		t.Errorf("status after next update = %q", got)
	}
	n := 0
	for _, e := range drain(ch) {
		if e.Type == sdk.EventForcedShutdown {
			n++
		}
	}
	if n != 1 {
		t.Errorf("forced_shutdown events = %d, want 1", n)
	}
}
