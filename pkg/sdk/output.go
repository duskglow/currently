package sdk

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Output publishes UPS state somewhere: a network protocol (REST, NUT,
// SNMP), a metrics endpoint, a message broker or a notifier.
type Output interface {
	Info() Info

	// ValidateConfig checks the output's settings without starting it.
	ValidateConfig(cfg Config) error

	// Run serves until ctx is cancelled. host gives read access to UPS
	// state and lets the output run commands on behalf of its clients.
	Run(ctx context.Context, host Host, cfg Config, log *slog.Logger) error
}

// Host is the view of the core that outputs use.
type Host interface {
	// ListUPS returns a snapshot of every configured UPS, in config order.
	ListUPS() []UPS
	// GetUPS returns a snapshot of one UPS.
	GetUPS(name string) (UPS, bool)
	// History returns recent samples of a numeric variable, oldest first.
	History(ups, variable string) ([]Sample, error)
	// RecentEvents returns up to n recent notable events, oldest first.
	RecentEvents(n int) []Event
	// Subscribe delivers events until ctx is cancelled, then closes the
	// channel. If the subscriber falls more than buffer events behind,
	// events are dropped rather than blocking the core.
	Subscribe(ctx context.Context, buffer int) <-chan Event
	// RunCommand runs an instant command on a UPS.
	RunCommand(ctx context.Context, ups, command, param string) error
	// SetVar changes a writable variable on a UPS.
	SetVar(ctx context.Context, ups, variable, value string) error
	// SetForcedShutdown adds the FSD flag to a UPS's status, telling every
	// monitoring client to shut down. As in NUT, the flag stays set until
	// nutng restarts.
	SetForcedShutdown(ctx context.Context, ups string) error
}

// ConnState is the state of the link between the core and a UPS.
type ConnState string

const (
	// StateConnecting: the driver is opening the device.
	StateConnecting ConnState = "connecting"
	// StateConnected: data is arriving.
	StateConnected ConnState = "connected"
	// StateStale: the device is open but has stopped reporting.
	StateStale ConnState = "stale"
	// StateFailed: the driver failed and will retry.
	StateFailed ConnState = "failed"
)

// UPS is a point-in-time snapshot of one UPS.
type UPS struct {
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Driver      string              `json:"driver"`
	State       ConnState           `json:"state"`
	LastError   string              `json:"last_error,omitempty"`
	LastUpdate  time.Time           `json:"last_update"`
	Restarts    int                 `json:"restarts"`
	Vars        map[string]Variable `json:"vars"`
	Commands    []Command           `json:"commands"`
}

// Status returns the parsed ups.status variable.
func (u UPS) Status() Status { return ParseStatus(u.Vars["ups.status"].Value) }

// Value returns a variable's value, or "" when it is not reported.
func (u UPS) Value(name string) string { return u.Vars[name].Value }

// Float returns a numeric variable's value.
func (u UPS) Float(name string) (float64, bool) {
	v, ok := u.Vars[name]
	if !ok {
		return 0, false
	}
	return v.Float()
}

// Model returns the best available "manufacturer model" text.
func (u UPS) Model() string {
	mfr := firstNonEmpty(u.Value("device.mfr"), u.Value("ups.mfr"))
	model := firstNonEmpty(u.Value("device.model"), u.Value("ups.model"))
	if mfr != "" && model != "" && !strings.HasPrefix(strings.ToLower(model), strings.ToLower(mfr)) {
		return mfr + " " + model
	}
	return firstNonEmpty(model, mfr)
}

// Sample is one point of a variable's history.
type Sample struct {
	Time  time.Time `json:"t"`
	Value float64   `json:"v"`
}

// EventType names what happened.
type EventType string

const (
	// EventUpdate: variables changed. Event.Changed holds the new values.
	EventUpdate EventType = "update"
	// Power events, derived from changes in ups.status.
	EventOnBattery      EventType = "on_battery"
	EventOnLine         EventType = "on_line"
	EventLowBattery     EventType = "low_battery"
	EventReplaceBattery EventType = "replace_battery"
	EventOverload       EventType = "overload"
	EventForcedShutdown EventType = "forced_shutdown"
	// Link events.
	EventCommLost     EventType = "comm_lost"
	EventCommRestored EventType = "comm_restored"
	EventDriverError  EventType = "driver_error"
	// EventCommand: an instant command or variable change was sent.
	EventCommand EventType = "command"
)

// Notable reports whether the event is worth showing to a person. Only
// EventUpdate is not.
func (t EventType) Notable() bool { return t != EventUpdate }

// Event is something that happened to a UPS.
type Event struct {
	Seq     uint64            `json:"seq"`
	Time    time.Time         `json:"time"`
	UPS     string            `json:"ups"`
	Type    EventType         `json:"type"`
	Message string            `json:"message,omitempty"`
	Changed map[string]string `json:"changed,omitempty"`
}

// FormatSeconds renders a seconds value (as reported in battery.runtime)
// as a short duration such as "42m" or "1h05m".
func FormatSeconds(s string) string {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return s
	}
	d := time.Duration(f) * time.Second
	switch {
	case d >= time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h" + pad2(int(d.Minutes())%60) + "m"
	case d >= time.Minute:
		return strconv.Itoa(int(d.Minutes())) + "m" + pad2(int(d.Seconds())%60) + "s"
	default:
		return strconv.Itoa(int(d.Seconds())) + "s"
	}
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
