// Package core runs the drivers, keeps UPS state and implements sdk.Host
// for outputs.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/nut-ng/nut-ng/internal/suggest"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// UPSSpec configures one UPS.
type UPSSpec struct {
	Name        string
	Description string
	Driver      sdk.Driver
	Config      sdk.Config
	// StaleAfter is how long the UPS may stay silent before it is marked
	// stale. Zero disables the check.
	StaleAfter time.Duration
}

// Core owns the state store, the event bus and one runner per UPS.
type Core struct {
	log   *slog.Logger
	bus   *Bus
	store *Store

	mu      sync.Mutex
	runners map[string]*runner
	order   []string

	// Backoff bounds for reopening a failed device.
	MinBackoff, MaxBackoff time.Duration
}

var _ sdk.Host = (*Core)(nil)

// New returns an empty core.
func New(log *slog.Logger) *Core {
	bus := NewBus()
	return &Core{
		log:        log,
		bus:        bus,
		store:      NewStore(bus),
		runners:    map[string]*runner{},
		MinBackoff: time.Second,
		MaxBackoff: time.Minute,
	}
}

// AddUPS registers a UPS. Call before Run.
func (c *Core) AddUPS(spec UPSSpec) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, dup := c.runners[spec.Name]; dup {
		return fmt.Errorf("UPS %q is defined twice", spec.Name)
	}
	c.runners[spec.Name] = &runner{core: c, spec: spec,
		log: c.log.With("ups", spec.Name, "driver", spec.Driver.Info().Name)}
	c.order = append(c.order, spec.Name)
	c.store.Add(spec.Name, spec.Description, spec.Driver.Info().Name, spec.StaleAfter)
	return nil
}

// Run starts every driver and blocks until ctx is cancelled and all drivers
// have stopped.
func (c *Core) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	c.mu.Lock()
	for _, name := range c.order {
		r := c.runners[name]
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.run(ctx)
		}()
	}
	c.mu.Unlock()

	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.store.CheckStale()
			}
		}
	}()
	wg.Wait()
	return nil
}

// ListUPS implements sdk.Host.
func (c *Core) ListUPS() []sdk.UPS { return c.store.List() }

// GetUPS implements sdk.Host.
func (c *Core) GetUPS(name string) (sdk.UPS, bool) { return c.store.Get(name) }

// History implements sdk.Host.
func (c *Core) History(ups, variable string) ([]sdk.Sample, error) {
	return c.store.History(ups, variable)
}

// RecentEvents implements sdk.Host.
func (c *Core) RecentEvents(n int) []sdk.Event { return c.bus.Recent(n) }

// Subscribe implements sdk.Host.
func (c *Core) Subscribe(ctx context.Context, buffer int) <-chan sdk.Event {
	return c.bus.Subscribe(ctx, buffer)
}

// NotFoundError returns an sdk.ErrNotFound error for an unknown UPS name,
// with a suggestion when the name looks like a typo.
func (c *Core) NotFoundError(name string) error {
	return fmt.Errorf("no UPS named %q%s: %w", name, suggest.DidYouMean(name, c.store.Names()), sdk.ErrNotFound)
}

// RunCommand implements sdk.Host.
func (c *Core) RunCommand(ctx context.Context, ups, command, param string) error {
	r, dev, err := c.device(ups)
	if err != nil {
		return err
	}
	names := make([]string, 0)
	for _, cmd := range dev.Commands() {
		names = append(names, cmd.Name)
	}
	if !slices.Contains(names, command) {
		return fmt.Errorf("UPS %s has no command %q%s: %w", ups, command, suggest.DidYouMean(command, names), sdk.ErrNotFound)
	}
	if err := dev.RunCommand(ctx, command, param); err != nil {
		return err
	}
	msg := fmt.Sprintf("Ran %s on UPS %s", command, ups)
	if param != "" {
		msg = fmt.Sprintf("Ran %s %s on UPS %s", command, param, ups)
	}
	r.log.Info("command run", "command", command, "param", param)
	c.bus.Publish(sdk.Event{UPS: ups, Type: sdk.EventCommand, Message: msg})
	return nil
}

// SetVar implements sdk.Host.
func (c *Core) SetVar(ctx context.Context, ups, variable, value string) error {
	r, dev, err := c.device(ups)
	if err != nil {
		return err
	}
	writable := dev.Writable()
	if !slices.Contains(writable, variable) {
		return fmt.Errorf("variable %q of UPS %s is not writable%s: %w", variable, ups, suggest.DidYouMean(variable, writable), sdk.ErrNotSupported)
	}
	if err := dev.SetVar(ctx, variable, value); err != nil {
		return err
	}
	r.log.Info("variable set", "variable", variable, "value", value)
	c.bus.Publish(sdk.Event{UPS: ups, Type: sdk.EventCommand,
		Message: fmt.Sprintf("Set %s to %s on UPS %s", variable, value, ups)})
	return nil
}

func (c *Core) device(ups string) (*runner, sdk.Device, error) {
	c.mu.Lock()
	r := c.runners[ups]
	c.mu.Unlock()
	if r == nil {
		return nil, nil, c.NotFoundError(ups)
	}
	dev := r.current()
	if dev == nil {
		return nil, nil, fmt.Errorf("UPS %s: %w", ups, sdk.ErrNotConnected)
	}
	return r, dev, nil
}

// runner keeps one UPS's device open, reopening it with backoff on failure.
type runner struct {
	core *Core
	spec UPSSpec
	log  *slog.Logger

	mu  sync.Mutex
	dev sdk.Device
}

func (r *runner) current() sdk.Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dev
}

func (r *runner) setDevice(d sdk.Device) {
	r.mu.Lock()
	r.dev = d
	r.mu.Unlock()
}

func (r *runner) run(ctx context.Context) {
	store := r.core.store
	backoff := r.core.MinBackoff
	for {
		store.SetConnecting(r.spec.Name)
		started := time.Now()
		err := r.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("driver stopped reporting")
		}
		if time.Since(started) > r.core.MaxBackoff {
			backoff = r.core.MinBackoff // it ran for a while; start over
		}
		r.log.Warn("UPS driver failed; retrying", "error", err, "retry_in", backoff)
		store.SetFailed(r.spec.Name, err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, r.core.MaxBackoff)
	}
}

// session opens the device and watches it until it fails or ctx ends.
func (r *runner) session(ctx context.Context) error {
	dev, err := r.spec.Driver.Open(ctx, r.spec.Config, r.log)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() {
		r.setDevice(nil)
		if err := dev.Close(); err != nil {
			r.log.Debug("close failed", "error", err)
		}
	}()
	r.core.store.SetDevice(r.spec.Name, dev.Commands(), dev.Writable())
	r.setDevice(dev)
	r.log.Info("UPS driver connected")
	return dev.Watch(ctx, func(u sdk.Update) { r.core.store.Apply(r.spec.Name, u) })
}
