package core

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/duskglow/currently/internal/suggest"
	"github.com/duskglow/currently/pkg/sdk"
)

// Store holds the latest state of every UPS, a short history of numeric
// variables, and derives power events from status changes.
type Store struct {
	mu    sync.RWMutex
	ups   map[string]*upsState
	order []string
	bus   *Bus
	now   func() time.Time

	historyLen      int
	historyInterval time.Duration
}

type upsState struct {
	name, description, driver string
	staleAfter                time.Duration

	state      sdk.ConnState
	lastErr    string
	lastUpdate time.Time
	restarts   int
	vars       map[string]sdk.Variable
	commands   []sdk.Command
	writable   map[string]bool
	history    map[string]*ring
	fsd        bool // forced shutdown requested; kept in ups.status
}

// NewStore returns a store that publishes events on bus.
func NewStore(bus *Bus) *Store {
	return &Store{
		ups:             map[string]*upsState{},
		bus:             bus,
		now:             time.Now,
		historyLen:      360,
		historyInterval: 10 * time.Second, // 360 × 10s = one hour
	}
}

// Add registers a UPS. staleAfter is how long the UPS may stay silent
// before it is marked stale.
func (s *Store) Add(name, description, driver string, staleAfter time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ups[name]; !ok {
		s.order = append(s.order, name)
	}
	s.ups[name] = &upsState{
		name:        name,
		description: description,
		driver:      driver,
		staleAfter:  staleAfter,
		state:       sdk.StateConnecting,
		vars:        map[string]sdk.Variable{},
		writable:    map[string]bool{},
		history:     map[string]*ring{},
	}
}

// Names returns the UPS names in registration order.
func (s *Store) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.order)
}

// SetDevice records the commands and writable variables of a newly opened
// device.
func (s *Store) SetDevice(name string, commands []sdk.Command, writable []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.ups[name]
	if u == nil {
		return
	}
	u.commands = slices.Clone(commands)
	u.writable = map[string]bool{}
	for _, w := range writable {
		u.writable[w] = true
	}
	for k, v := range u.vars {
		v.Writable = u.writable[k]
		u.vars[k] = v
	}
}

// Apply records an update from a UPS's driver.
func (s *Store) Apply(name string, up sdk.Update) {
	s.mu.Lock()
	u := s.ups[name]
	if u == nil {
		s.mu.Unlock()
		return
	}
	now := s.now()
	var events []sdk.Event

	switch u.state {
	case sdk.StateStale:
		events = append(events, sdk.Event{UPS: name, Type: sdk.EventCommRestored,
			Message: fmt.Sprintf("UPS %s is reporting again", name)})
	case sdk.StateFailed, sdk.StateConnecting:
		if u.restarts > 0 {
			events = append(events, sdk.Event{UPS: name, Type: sdk.EventCommRestored,
				Message: fmt.Sprintf("UPS %s reconnected", name)})
		}
	}
	u.state = sdk.StateConnected
	u.lastErr = ""
	u.lastUpdate = now

	oldStatus := sdk.ParseStatus(u.vars["ups.status"].Value)
	_, hadStatus := u.vars["ups.status"]

	changed := map[string]string{}
	for k, val := range up.Vars {
		old, ok := u.vars[k]
		if !ok || old.Value != val {
			changed[k] = val
		}
		spec, _ := sdk.LookupVar(k)
		v := sdk.Variable{Name: k, Value: val, Unit: spec.Unit, Writable: u.writable[k], Updated: now}
		u.vars[k] = v
		if f, ok := v.Float(); ok && (spec.Numeric || spec.Name == "") {
			s.record(u, k, now, f)
		}
	}
	for _, k := range up.Removed {
		if _, ok := u.vars[k]; ok {
			delete(u.vars, k)
			changed[k] = ""
		}
	}
	if u.fsd {
		if st := u.vars["ups.status"]; !sdk.ParseStatus(st.Value).Has(sdk.FlagForcedShutdown) {
			st.Name, st.Updated = "ups.status", now
			st.Value = strings.TrimSpace(st.Value + " " + string(sdk.FlagForcedShutdown))
			u.vars["ups.status"] = st
			if old := oldStatus.String(); st.Value == old {
				delete(changed, "ups.status")
			} else {
				changed["ups.status"] = st.Value
			}
		}
	}

	if st, ok := changed["ups.status"]; ok {
		events = append(events, statusEvents(name, oldStatus, sdk.ParseStatus(st), hadStatus)...)
	}
	if len(changed) > 0 {
		events = append([]sdk.Event{{UPS: name, Type: sdk.EventUpdate, Changed: changed, Time: now}}, events...)
	}
	s.mu.Unlock()

	for _, e := range events {
		s.bus.Publish(e)
	}
}

// statusEvents derives power events from a change in ups.status.
func statusEvents(name string, old, cur sdk.Status, hadOld bool) []sdk.Event {
	var events []sdk.Event
	add := func(t sdk.EventType, format string) {
		events = append(events, sdk.Event{UPS: name, Type: t, Message: fmt.Sprintf(format, name)})
	}
	appeared := func(f sdk.Flag) bool { return cur.Has(f) && !old.Has(f) }

	if appeared(sdk.FlagOnBattery) {
		add(sdk.EventOnBattery, "UPS %s is on battery")
	}
	if hadOld && old.Has(sdk.FlagOnBattery) && !cur.Has(sdk.FlagOnBattery) && cur.Has(sdk.FlagOnline) {
		add(sdk.EventOnLine, "UPS %s is back on line power")
	}
	if appeared(sdk.FlagLowBattery) {
		add(sdk.EventLowBattery, "UPS %s battery is low")
	}
	if appeared(sdk.FlagReplaceBattery) {
		add(sdk.EventReplaceBattery, "UPS %s battery needs replacing")
	}
	if appeared(sdk.FlagOverload) {
		add(sdk.EventOverload, "UPS %s is overloaded")
	}
	if appeared(sdk.FlagForcedShutdown) {
		add(sdk.EventForcedShutdown, "UPS %s forced shutdown in progress")
	}
	return events
}

// SetFSD sets the forced-shutdown flag on a UPS.
func (s *Store) SetFSD(name string) error {
	s.mu.Lock()
	u := s.ups[name]
	if u == nil {
		s.mu.Unlock()
		return fmt.Errorf("UPS %q: %w", name, sdk.ErrNotFound)
	}
	if u.fsd {
		s.mu.Unlock()
		return nil
	}
	u.fsd = true
	st := u.vars["ups.status"]
	st.Name, st.Updated = "ups.status", s.now()
	st.Value = strings.TrimSpace(st.Value + " " + string(sdk.FlagForcedShutdown))
	u.vars["ups.status"] = st
	s.mu.Unlock()

	s.bus.Publish(sdk.Event{UPS: name, Type: sdk.EventUpdate, Changed: map[string]string{"ups.status": st.Value}})
	s.bus.Publish(sdk.Event{UPS: name, Type: sdk.EventForcedShutdown,
		Message: fmt.Sprintf("Forced shutdown requested for UPS %s", name)})
	return nil
}

// SetFailed records that the driver failed and will be retried.
func (s *Store) SetFailed(name string, err error) {
	s.mu.Lock()
	u := s.ups[name]
	if u == nil {
		s.mu.Unlock()
		return
	}
	wasConnected := u.state == sdk.StateConnected || u.state == sdk.StateStale
	u.state = sdk.StateFailed
	u.lastErr = err.Error()
	u.restarts++
	s.mu.Unlock()

	s.bus.Publish(sdk.Event{UPS: name, Type: sdk.EventDriverError, Message: err.Error()})
	if wasConnected {
		s.bus.Publish(sdk.Event{UPS: name, Type: sdk.EventCommLost,
			Message: fmt.Sprintf("Lost contact with UPS %s", name)})
	}
}

// SetConnecting records that the driver is (re)opening the device.
func (s *Store) SetConnecting(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u := s.ups[name]; u != nil && u.state != sdk.StateFailed {
		u.state = sdk.StateConnecting
	}
}

// CheckStale marks connected UPSes that have stopped reporting as stale.
func (s *Store) CheckStale() {
	now := s.now()
	var lost []string
	s.mu.Lock()
	for _, name := range s.order {
		u := s.ups[name]
		if u.state == sdk.StateConnected && u.staleAfter > 0 && now.Sub(u.lastUpdate) > u.staleAfter {
			u.state = sdk.StateStale
			u.lastErr = fmt.Sprintf("no data for %s", now.Sub(u.lastUpdate).Round(time.Second))
			lost = append(lost, name)
		}
	}
	s.mu.Unlock()
	for _, name := range lost {
		s.bus.Publish(sdk.Event{UPS: name, Type: sdk.EventCommLost,
			Message: fmt.Sprintf("UPS %s stopped reporting", name)})
	}
}

// Get returns a snapshot of one UPS.
func (s *Store) Get(name string) (sdk.UPS, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.ups[name]
	if u == nil {
		return sdk.UPS{}, false
	}
	return u.snapshot(), true
}

// List returns snapshots of all UPSes in registration order.
func (s *Store) List() []sdk.UPS {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]sdk.UPS, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, s.ups[name].snapshot())
	}
	return out
}

// Writable reports whether a variable of a UPS accepts SetVar.
func (s *Store) Writable(name, variable string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.ups[name]
	return u != nil && u.writable[variable]
}

// History returns recorded samples of a variable, oldest first.
func (s *Store) History(name, variable string) ([]sdk.Sample, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.ups[name]
	if u == nil {
		return nil, fmt.Errorf("UPS %q%s: %w", name, suggest.DidYouMean(name, s.order), sdk.ErrNotFound)
	}
	r := u.history[variable]
	if r == nil {
		names := slices.Sorted(maps.Keys(u.history))
		return nil, fmt.Errorf("no history for %q%s: %w", variable, suggest.DidYouMean(variable, names), sdk.ErrNotFound)
	}
	return r.samples(), nil
}

func (s *Store) record(u *upsState, name string, t time.Time, v float64) {
	r := u.history[name]
	if r == nil {
		r = newRing(s.historyLen)
		u.history[name] = r
	}
	if last, ok := r.last(); ok && t.Sub(last.Time) < s.historyInterval {
		return
	}
	r.add(sdk.Sample{Time: t, Value: v})
}

func (u *upsState) snapshot() sdk.UPS {
	return sdk.UPS{
		Name:        u.name,
		Description: u.description,
		Driver:      u.driver,
		State:       u.state,
		LastError:   u.lastErr,
		LastUpdate:  u.lastUpdate,
		Restarts:    u.restarts,
		Vars:        maps.Clone(u.vars),
		Commands:    slices.Clone(u.commands),
	}
}

// ring is a fixed-size buffer of samples.
type ring struct {
	buf  []sdk.Sample
	next int
	n    int
}

func newRing(size int) *ring { return &ring{buf: make([]sdk.Sample, size)} }

func (r *ring) add(s sdk.Sample) {
	r.buf[r.next] = s
	r.next = (r.next + 1) % len(r.buf)
	r.n = min(r.n+1, len(r.buf))
}

func (r *ring) last() (sdk.Sample, bool) {
	if r.n == 0 {
		return sdk.Sample{}, false
	}
	return r.buf[(r.next-1+len(r.buf))%len(r.buf)], true
}

func (r *ring) samples() []sdk.Sample {
	out := make([]sdk.Sample, 0, r.n)
	start := (r.next - r.n + len(r.buf)) % len(r.buf)
	for i := 0; i < r.n; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}
