package plugins

import (
	"context"
	"io"
	"log"
	"log/slog"

	"github.com/hashicorp/go-hclog"
)

// hclogAdapter sends go-plugin's logs (including the plugin's own log
// lines) to slog.
type hclogAdapter struct {
	l      *slog.Logger
	name   string
	args   []any
	plugin func() string // the plugin's current name
}

var _ hclog.Logger = (*hclogAdapter)(nil)

func newHCLog(l *slog.Logger, plugin func() string) hclog.Logger {
	return &hclogAdapter{l: l, plugin: plugin}
}

func (h *hclogAdapter) Log(level hclog.Level, msg string, args ...any) {
	var lvl slog.Level
	switch level {
	case hclog.Trace, hclog.Debug:
		lvl = slog.LevelDebug
	case hclog.Warn:
		lvl = slog.LevelWarn
	case hclog.Error:
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	all := []any{"plugin", h.plugin()}
	all = append(all, h.args...)
	for i := 0; i+1 < len(args); i += 2 {
		k, _ := args[i].(string)
		switch k {
		case "timestamp": // the plugin's (missing) timestamp
			continue
		case "plugin": // go-plugin's executable path
			k = "path"
		}
		all = append(all, k, args[i+1])
	}
	h.l.Log(context.Background(), lvl, msg, all...)
}

func (h *hclogAdapter) Trace(msg string, args ...any) { h.Log(hclog.Trace, msg, args...) }
func (h *hclogAdapter) Debug(msg string, args ...any) { h.Log(hclog.Debug, msg, args...) }
func (h *hclogAdapter) Info(msg string, args ...any)  { h.Log(hclog.Info, msg, args...) }
func (h *hclogAdapter) Warn(msg string, args ...any)  { h.Log(hclog.Warn, msg, args...) }
func (h *hclogAdapter) Error(msg string, args ...any) { h.Log(hclog.Error, msg, args...) }

func (h *hclogAdapter) IsTrace() bool { return false }
func (h *hclogAdapter) IsDebug() bool { return h.l.Enabled(context.Background(), slog.LevelDebug) }
func (h *hclogAdapter) IsInfo() bool  { return true }
func (h *hclogAdapter) IsWarn() bool  { return true }
func (h *hclogAdapter) IsError() bool { return true }

func (h *hclogAdapter) ImpliedArgs() []any { return h.args }
func (h *hclogAdapter) With(args ...any) hclog.Logger {
	return &hclogAdapter{l: h.l, name: h.name, args: append(append([]any{}, h.args...), args...), plugin: h.plugin}
}
func (h *hclogAdapter) Name() string { return h.name }
func (h *hclogAdapter) Named(name string) hclog.Logger {
	return &hclogAdapter{l: h.l, name: name, args: h.args, plugin: h.plugin}
}
func (h *hclogAdapter) ResetNamed(name string) hclog.Logger {
	return &hclogAdapter{l: h.l, name: name, args: h.args, plugin: h.plugin}
}
func (h *hclogAdapter) SetLevel(hclog.Level)  {}
func (h *hclogAdapter) GetLevel() hclog.Level { return hclog.Info }
func (h *hclogAdapter) StandardLogger(*hclog.StandardLoggerOptions) *log.Logger {
	return log.New(h.StandardWriter(nil), "", 0)
}
func (h *hclogAdapter) StandardWriter(*hclog.StandardLoggerOptions) io.Writer { return writer{h} }

type writer struct{ h *hclogAdapter }

func (w writer) Write(p []byte) (int, error) {
	w.h.Info(string(p))
	return len(p), nil
}
