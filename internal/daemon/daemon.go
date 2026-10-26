// Package daemon assembles the core, drivers and outputs from a config and
// runs them.
package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/nut-ng/nut-ng/internal/config"
	"github.com/nut-ng/nut-ng/internal/core"
	"github.com/nut-ng/nut-ng/internal/policy"
	"github.com/nut-ng/nut-ng/internal/registry"
	"github.com/nut-ng/nut-ng/outputs/nutproto"
	"github.com/nut-ng/nut-ng/outputs/prometheus"
	"github.com/nut-ng/nut-ng/outputs/rest"
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
	// Executor replaces the one that really runs shutdown commands (tests).
	Executor policy.Executor
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

	// Open every listener before starting anything, so a busy port fails
	// fast with a clear message.
	ctl, ctlPath, err := listenControl(cfg.Listen.Control)
	if err != nil {
		return err
	}
	defer ctl.Close()
	var httpLn, metricsLn, nutLn net.Listener
	closeAll := func() {
		for _, ln := range []net.Listener{httpLn, metricsLn, nutLn} {
			if ln != nil {
				ln.Close()
			}
		}
	}
	defer closeAll()
	if cfg.Listen.HTTP != "" {
		if httpLn, err = listenTCP(cfg.Listen.HTTP, "the REST API", "listen.http", "another nutng"); err != nil {
			return err
		}
	}
	separateMetrics := cfg.Listen.Metrics != "" && !strings.EqualFold(cfg.Listen.Metrics, "off")
	if separateMetrics {
		if metricsLn, err = listenTCP(cfg.Listen.Metrics, "Prometheus metrics", "listen.metrics", "another nutng"); err != nil {
			return err
		}
	}
	var nutTLS *tls.Config
	if t := cfg.Listen.NUTTLS; t != nil && cfg.Listen.NUT != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return fmt.Errorf("listen.nut_tls: %w", err)
		}
		nutTLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	if cfg.Listen.NUT != "" {
		if nutLn, err = listenTCP(cfg.Listen.NUT, "the NUT protocol", "listen.nut", "NUT's upsd or another nutng"); err != nil {
			return err
		}
	}

	// The NUT server tells the shutdown policy which clients are logged in.
	var nutSrv *nutproto.Server
	if nutLn != nil {
		users := map[string]nutproto.User{}
		for name, u := range cfg.Users {
			users[name] = nutproto.User{Password: u.Secret(), Role: u.Role}
		}
		nutSrv = nutproto.New(c, nutproto.Options{Users: users, TLS: nutTLS, Version: o.Version, Log: log.With("output", "nut")})
	}
	popts := policy.Options{Host: c, Executor: o.Executor, Log: log.With("component", "shutdown")}
	if popts.Executor == nil {
		popts.Executor = policy.NewExecutor(popts.Log)
	}
	if nutSrv != nil {
		popts.Clients = nutSrv.Clients
	}
	engine := policy.New(PolicyConfig(cfg), popts)
	shutdownStatus := func() any { return engine.Status() }

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return c.Run(ctx) })
	g.Go(func() error { return engine.Run(ctx) })
	g.Go(func() error {
		h := rest.NewHandler(c, rest.Options{Trusted: true, Version: o.Version, Shutdown: shutdownStatus, Log: log.With("api", "control")})
		return rest.Serve(ctx, ctl, h)
	})
	if httpLn != nil {
		api := rest.NewHandler(c, rest.Options{Token: cfg.Token(), Version: o.Version, Shutdown: shutdownStatus, Log: log.With("api", "http")})
		mux := http.NewServeMux()
		mux.Handle("/", api)
		if cfg.Listen.Metrics == "" {
			mux.Handle("GET /metrics", prometheus.Handler(c, o.Version))
		}
		log.Info("REST API listening", "addr", httpLn.Addr().String(), "metrics", cfg.Listen.Metrics == "",
			"changes_allowed", cfg.Token() != "")
		g.Go(func() error { return rest.Serve(ctx, httpLn, mux) })
	}
	if metricsLn != nil {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", prometheus.Handler(c, o.Version))
		log.Info("Prometheus metrics listening", "addr", metricsLn.Addr().String())
		g.Go(func() error { return rest.Serve(ctx, metricsLn, mux) })
	}
	if nutSrv != nil {
		log.Info("NUT protocol listening", "addr", nutLn.Addr().String(), "users", len(cfg.Users), "starttls", nutTLS != nil)
		g.Go(func() error { return nutSrv.Serve(ctx, nutLn) })
	}
	if cfg.Shutdown == nil {
		log.Info("no shutdown policy: nutng will not shut this computer down (add a shutdown section to nutng.yaml)")
	} else if why := policy.BuiltinBlocked(); why != "" && cfg.Shutdown.Command == "" {
		log.Warn("the operating system shutdown command is blocked: " + why)
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
	if len(path) > config.MaxSocketPath() {
		return nil, fmt.Errorf("socket path %s is %d characters long; the limit is %d", path, len(path), config.MaxSocketPath())
	}
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
		// Owner and group may use the CLI. When a nutng group exists, its
		// members can control the daemon without root.
		_ = os.Chmod(path, 0o660)
		if g, err := user.LookupGroup("nutng"); err == nil {
			if gid, err := strconv.Atoi(g.Gid); err == nil {
				_ = os.Chown(path, -1, gid)
			}
		}
	}
	return ln, nil
}

// listenTCP opens a TCP listener, explaining the usual causes of failure.
func listenTCP(addr, what, key, usual string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, nil
	}
	if isAddrInUse(err) {
		return nil, fmt.Errorf("cannot serve %s on %s: the port is already in use. Is %s running? "+
			"Stop it, or set %s in nutng.yaml to another address or \"off\"", what, addr, usual, key)
	}
	return nil, fmt.Errorf("cannot serve %s on %s (%s): %w", what, addr, key, err)
}

func isAddrInUse(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") || // Unix
		strings.Contains(msg, "only one usage of each socket address") // Windows
}

func orNone(s string) string {
	if s == "" {
		return "(none, using defaults)"
	}
	return s
}
