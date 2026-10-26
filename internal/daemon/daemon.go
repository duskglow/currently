// Package daemon assembles the core, drivers and outputs from a config and
// runs them.
package daemon

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/nut-ng/nut-ng/internal/config"
	"github.com/nut-ng/nut-ng/internal/core"
	"github.com/nut-ng/nut-ng/internal/plugins"
	"github.com/nut-ng/nut-ng/internal/policy"
	"github.com/nut-ng/nut-ng/internal/registry"
	"github.com/nut-ng/nut-ng/outputs/nutproto"
	"github.com/nut-ng/nut-ng/outputs/prometheus"
	"github.com/nut-ng/nut-ng/outputs/rest"
	"github.com/nut-ng/nut-ng/outputs/snmp"
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
	// Executor replaces the one that really runs shutdown commands (tests).
	Executor policy.Executor
	// Plugins, if set, is reported by GET /api/v1/plugins.
	Plugins *plugins.Manager
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
	var snmpConn net.PacketConn
	closeAll := func() {
		for _, ln := range []net.Listener{httpLn, metricsLn, nutLn} {
			if ln != nil {
				ln.Close()
			}
		}
		if snmpConn != nil {
			snmpConn.Close()
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

	var agent *snmp.Agent
	if s := cfg.Listen.SNMP; s != nil {
		if snmpConn, err = listenUDP(s.Addr); err != nil {
			return err
		}
		if agent, err = snmp.New(c, SNMPOptions(s, o.Version, log.With("output", "snmp"))); err != nil {
			return fmt.Errorf("listen.snmp: %w", err)
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
	pluginStatus := func() any { return o.Plugins.Statuses() }

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return c.Run(ctx) })
	g.Go(func() error { return engine.Run(ctx) })
	g.Go(func() error {
		h := rest.NewHandler(c, rest.Options{Trusted: true, Version: o.Version, Shutdown: shutdownStatus, Plugins: pluginStatus, Log: log.With("api", "control")})
		return rest.Serve(ctx, ctl, h)
	})
	if httpLn != nil {
		api := rest.NewHandler(c, rest.Options{Token: cfg.Token(), Version: o.Version, Shutdown: shutdownStatus, Plugins: pluginStatus, Log: log.With("api", "http")})
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
	if agent != nil {
		s := cfg.Listen.SNMP
		log.Info("SNMP agent listening", "addr", snmpConn.LocalAddr().String(), "v2c", s.Secret() != "" || len(s.Users) == 0,
			"v3_users", len(s.Users), "traps", len(s.Traps))
		g.Go(func() error { return agent.Serve(ctx, snmpConn) })
	}
	used := map[string]bool{}
	for _, u := range cfg.UPS {
		used[u.Driver] = true
	}
	for name, oc := range cfg.Outputs {
		used[oc.PluginName(name)] = true
	}
	o.Plugins.StopUnused(used)

	for _, name := range cfg.OutputNames() {
		oc := cfg.Outputs[name]
		out, ok := o.Registry.Output(oc.PluginName(name))
		if !ok {
			return fmt.Errorf("output %s: no output plugin named %q", name, oc.PluginName(name))
		}
		var host sdk.Host = c
		if !oc.AllowControl {
			host = plugins.ReadOnlyHost{Host: c}
		}
		olog := log.With("output", name)
		g.Go(func() error { superviseOutput(ctx, out, host, oc.Options, olog); return nil })
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

// superviseOutput runs an output until ctx ends, restarting it with
// backoff when it fails. A failing output never stops nutng.
func superviseOutput(ctx context.Context, out sdk.Output, host sdk.Host, cfg sdk.Config, log *slog.Logger) {
	backoff := time.Second
	for {
		started := time.Now()
		log.Info("output started", "plugin", out.Info().Name)
		err := out.Run(ctx, host, cfg, log)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		log.Warn("output stopped; restarting it", "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
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
// SNMPOptions converts the listen.snmp section to agent options.
func SNMPOptions(s *config.SNMP, version string, log *slog.Logger) snmp.Options {
	o := snmp.Options{Community: s.Secret(), DefaultUPS: s.UPS, Location: s.Location, Contact: s.Contact,
		Version: version, Log: log}
	if o.Community == "" && len(s.Users) == 0 {
		o.Community = "public"
	}
	if s.EngineID != "" {
		o.EngineID, _ = hex.DecodeString(strings.TrimPrefix(strings.ReplaceAll(s.EngineID, ":", ""), "0x"))
	}
	for _, name := range slices.Sorted(maps.Keys(s.Users)) {
		u := s.Users[name]
		auth, priv := u.Secrets()
		o.Users = append(o.Users, snmp.User{Name: name, Auth: u.Auth, AuthPassword: auth, Privacy: u.Privacy, PrivacyPassword: priv})
	}
	for _, t := range s.Traps {
		community := t.Community
		if community == "" {
			community = "public"
		}
		o.Traps = append(o.Traps, snmp.TrapTarget{Addr: config.TrapAddr(t.Target), Community: community})
	}
	return o
}

func listenUDP(addr string) (net.PacketConn, error) {
	pc, err := net.ListenPacket("udp", addr)
	if err == nil {
		return pc, nil
	}
	hint := "is snmpd or another nutng using it? Choose another port with listen.snmp.addr"
	if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) {
		hint = "ports below 1024 need root (on Linux, or the CAP_NET_BIND_SERVICE capability); use a port such as 1161"
	}
	return nil, fmt.Errorf("cannot serve the SNMP agent on %s (listen.snmp.addr): %w; %s", addr, err, hint)
}

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
