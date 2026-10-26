// Package svcmgr installs and controls Currently as a system service: systemd,
// OpenRC or SysV init on Linux, launchd on macOS, rc.d on FreeBSD, and the
// Service Control Manager on Windows.
package svcmgr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kardianos/service"
)

// Name is the service name.
const Name = "currently"

// systemdUnit replaces the library's default unit: restart after 5 seconds
// rather than 2 minutes (a UPS monitor should not stay down), start after
// the network so the API can bind, and read secrets from /etc/default/currently.
const systemdUnit = `[Unit]
Description={{Description}}
ConditionFileIsExecutable={{Path | cmdEscape}}
After=network-online.target
Wants=network-online.target

[Service]
ExecStart={{Path | cmdEscape}}{{range Arguments}} {{. | cmd}}{{end}}
{{if UserName}}User={{UserName}}
{{end}}Restart=always
RestartSec=5
EnvironmentFile=-/etc/default/currently

[Install]
WantedBy=multi-user.target
`

// Config returns the service definition. args are passed to the Currently
// executable, for example ["serve", "--config", "/etc/currently/currently.yaml"].
func Config(args []string, user string) *service.Config {
	return &service.Config{
		Name:        Name,
		DisplayName: "Currently UPS monitor",
		Description: "Monitors UPSes and serves their state to monitoring tools (Currently)",
		Arguments:   args,
		UserName:    user,
		Option: service.KeyValue{
			"SystemdScript":          systemdUnit,
			"OnFailure":              "restart", // Windows
			"OnFailureDelayDuration": "5s",
			"OnFailureResetPeriod":   60,
			"KeepAlive":              true, // launchd
			"RunAtLoad":              true,
		},
	}
}

// noop is a service.Interface for control-only use.
type noop struct{}

func (noop) Start(service.Service) error { return nil }
func (noop) Stop(service.Service) error  { return nil }

// Controller returns a handle for installing and controlling the service.
func Controller(args []string, user string) (service.Service, error) {
	return service.New(noop{}, Config(args, user))
}

// State describes the installed service.
type State string

const (
	StateRunning      State = "running"
	StateStopped      State = "stopped"
	StateNotInstalled State = "not installed"
	StateUnknown      State = "unknown"
)

// Status returns the service state.
func Status() (State, error) {
	s, err := Controller(nil, "")
	if err != nil {
		return StateUnknown, err
	}
	st, err := s.Status()
	switch {
	case errors.Is(err, service.ErrNotInstalled):
		return StateNotInstalled, nil
	case err != nil:
		return StateUnknown, err
	case st == service.StatusRunning:
		return StateRunning, nil
	case st == service.StatusStopped:
		return StateStopped, nil
	}
	return StateUnknown, nil
}

// Platform names the service manager in use, for messages.
func Platform() string { return service.Platform() }

// Interactive reports whether Currently runs from a terminal rather than under
// a service manager.
func Interactive() bool { return service.Interactive() }

// LogHint says where the service's logs go.
func LogHint() string {
	switch runtime.GOOS {
	case "windows":
		return "Event Viewer → Windows Logs → Application, source \"currently\""
	case "darwin":
		return "`log show --predicate 'process == \"currently\"'`"
	}
	if strings.Contains(service.Platform(), "systemd") {
		return "`journalctl -u currently`"
	}
	return "the system log (syslog)"
}

// PermissionHint returns advice when err looks like missing privileges.
func PermissionHint(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if errors.Is(err, os.ErrPermission) || strings.Contains(msg, "access is denied") ||
		strings.Contains(msg, "permission denied") || strings.Contains(msg, "operation not permitted") {
		if runtime.GOOS == "windows" {
			return "run this from a terminal opened with \"Run as administrator\""
		}
		return "run it with sudo"
	}
	return ""
}

// RiskyLocation reports why an executable path is a poor home for a
// service binary, or "" when it looks fine.
func RiskyLocation(exe string) string {
	lower := strings.ToLower(filepath.ToSlash(exe))
	for _, d := range []string{"/downloads/", "/temp/", "/tmp/", "/desktop/", "/go-build"} {
		if strings.Contains(lower, d) {
			return "it is in a download or temporary folder"
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(lower, strings.ToLower(filepath.ToSlash(home))+"/") {
		return "it is in your home folder"
	}
	return ""
}

// Run runs fn under the service manager until the manager stops the
// service. A failure of fn exits the process with status 1, so the
// manager's restart policy applies.
func Run(fn func(ctx context.Context) error, log *slog.Logger) error {
	p := &program{fn: fn, log: log}
	s, err := service.New(p, Config(nil, ""))
	if err != nil {
		return err
	}
	return s.Run()
}

type program struct {
	fn     func(context.Context) error
	log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

func (p *program) Start(service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.done = cancel, make(chan struct{})
	go func() {
		defer close(p.done)
		if err := p.fn(ctx); err != nil && ctx.Err() == nil {
			p.log.Error("Currently failed", "error", err)
			os.Exit(1)
		}
	}()
	return nil
}

func (p *program) Stop(service.Service) error {
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(15 * time.Second):
		p.log.Warn("Currently did not stop within 15s")
	}
	return nil
}

// EventLogHandler is a slog.Handler that writes to the service manager's
// log (the Windows Event Log), for when stderr goes nowhere.
type EventLogHandler struct {
	logger service.Logger
	opts   *slog.HandlerOptions
	wrap   func(slog.Handler) slog.Handler
	mu     *sync.Mutex
}

// NewEventLogHandler returns a handler writing to the Currently service log.
func NewEventLogHandler(opts *slog.HandlerOptions) (*EventLogHandler, error) {
	s, err := service.New(noop{}, Config(nil, ""))
	if err != nil {
		return nil, err
	}
	l, err := s.SystemLogger(nil)
	if err != nil {
		return nil, err
	}
	return &EventLogHandler{logger: l, opts: opts, wrap: func(h slog.Handler) slog.Handler { return h }, mu: &sync.Mutex{}}, nil
}

func (h *EventLogHandler) Enabled(_ context.Context, l slog.Level) bool {
	min := slog.LevelInfo
	if h.opts != nil && h.opts.Level != nil {
		min = h.opts.Level.Level()
	}
	return l >= min
}

func (h *EventLogHandler) Handle(ctx context.Context, r slog.Record) error {
	var buf bytes.Buffer
	opts := &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey) {
			return slog.Attr{} // the event log records both
		}
		return a
	}}
	if err := h.wrap(slog.NewTextHandler(&buf, opts)).Handle(ctx, r); err != nil {
		return err
	}
	msg := strings.TrimSpace(buf.String())
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case r.Level >= slog.LevelError:
		return h.logger.Error(msg)
	case r.Level >= slog.LevelWarn:
		return h.logger.Warning(msg)
	}
	return h.logger.Info(msg)
}

func (h *EventLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	prev := h.wrap
	c.wrap = func(x slog.Handler) slog.Handler { return prev(x).WithAttrs(attrs) }
	return &c
}

func (h *EventLogHandler) WithGroup(name string) slog.Handler {
	c := *h
	prev := h.wrap
	c.wrap = func(x slog.Handler) slog.Handler { return prev(x).WithGroup(name) }
	return &c
}

// Describe formats an error from the service manager with a hint.
func Describe(action string, err error) error {
	if hint := PermissionHint(err); hint != "" {
		return fmt.Errorf("%s the service: %v; %s", action, err, hint)
	}
	return fmt.Errorf("%s the service: %w", action, err)
}
