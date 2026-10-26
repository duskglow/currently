// Package daemon assembles the core, drivers and outputs from a config and
// runs them.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/nut-ng/nut-ng/internal/config"
	"github.com/nut-ng/nut-ng/internal/core"
	"github.com/nut-ng/nut-ng/internal/registry"
	"github.com/nut-ng/nut-ng/outputs/rest"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// Options for Run.
type Options struct {
	Config   *config.Config
	Registry *registry.Registry
	Log      *slog.Logger
	Version  string
	// Ready, if set, is called with the control socket path once nutng is
	// serving.
	Ready func(controlPath string)
}

// Run serves until ctx is cancelled. The config must already be validated.
func Run(ctx context.Context, o Options) error {
	cfg, log := o.Config, o.Log

	c := core.New(log)
	for _, name := range cfg.UPSNames() {
		u := cfg.UPS[name]
		d, ok := o.Registry.Driver(u.Driver)
		if !ok {
			return fmt.Errorf("UPS %s: unknown driver %q", name, u.Driver)
		}
		if err := c.AddUPS(core.UPSSpec{Name: name, Description: u.Description, Driver: d,
			Config: u.Options, StaleAfter: u.StaleAfter}); err != nil {
			return err
		}
	}

	ctl, ctlPath, err := listenControl(cfg.Listen.Control)
	if err != nil {
		return err
	}
	defer ctl.Close()

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return c.Run(ctx) })
	g.Go(func() error {
		h := rest.NewHandler(c, rest.Options{Trusted: true, Version: o.Version, Log: log.With("api", "control")})
		return rest.Serve(ctx, ctl, h)
	})
	if cfg.Listen.HTTP != "" {
		out := &rest.Output{Version: o.Version}
		ocfg := sdk.Config{"addr": cfg.Listen.HTTP, "token": cfg.Token()}
		g.Go(func() error {
			if err := out.Run(ctx, c, ocfg, log.With("output", "rest")); err != nil {
				return fmt.Errorf("REST API: %w", err)
			}
			return nil
		})
	}

	log.Info("nutng started", "version", o.Version, "config", orNone(cfg.Path),
		"ups", len(cfg.UPS), "control", ctlPath)
	if len(cfg.UPS) == 0 {
		log.Warn("no UPS configured; add one to " + config.DefaultPath() +
			", or run `nutng serve --demo` to try nutng with a simulated UPS")
	}
	if o.Ready != nil {
		o.Ready(ctlPath)
	}
	err = g.Wait()
	log.Info("nutng stopped")
	return err
}

// listenControl opens the control socket at path, or at the first usable
// default location when path is empty.
func listenControl(path string) (net.Listener, string, error) {
	candidates := []string{path}
	if path == "" {
		candidates = config.ControlCandidates()
	}
	var errs []error
	for _, p := range candidates {
		ln, err := listenUnix(p)
		if err == nil {
			return ln, p, nil
		}
		if errors.Is(err, errInUse) {
			return nil, "", err
		}
		errs = append(errs, err)
	}
	return nil, "", fmt.Errorf("cannot create the control socket: %w", errors.Join(errs...))
}

var errInUse = errors.New("another nutng daemon is already running")

func listenUnix(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		// A socket file exists: in use by a live daemon, or left over from a
		// crash.
		if conn, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			conn.Close()
			return nil, fmt.Errorf("%w (its control socket %s is answering); stop it first", errInUse, path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket %s: %w", path, err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		// Owner and group may use the CLI; on Linux packages add admins to
		// the nutng group.
		_ = os.Chmod(path, 0o660)
	}
	return ln, nil
}

func orNone(s string) string {
	if s == "" {
		return "(none, using defaults)"
	}
	return s
}
