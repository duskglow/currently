// Package rest serves UPS state as a JSON API over HTTP, with a
// Server-Sent Events stream of changes. The same handler serves the local
// control socket used by the nutng CLI.
package rest

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nut-ng/nut-ng/internal/suggest"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

//go:embed openapi.yaml
var openapi []byte

var didYouMean = suggest.DidYouMean

// Options configure the handler.
type Options struct {
	// Token is the bearer token required for commands and variable changes.
	// With no token, changes are refused unless Trusted is set.
	Token string
	// Trusted allows changes without a token. Set it only for the local
	// control socket, which is protected by file permissions.
	Trusted bool
	// Version is reported by GET /api/v1.
	Version string
	Log     *slog.Logger
}

type handler struct {
	host sdk.Host
	opts Options
	mux  *http.ServeMux
}

// NewHandler returns the API handler.
func NewHandler(host sdk.Host, opts Options) http.Handler {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	h := &handler{host: host, opts: opts, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	h.mux.HandleFunc("GET /api/v1", h.index)
	h.mux.HandleFunc("GET /api/v1/{$}", h.index)
	h.mux.HandleFunc("GET /api/v1/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(openapi)
	})
	h.mux.HandleFunc("GET /api/v1/ups", h.listUPS)
	h.mux.HandleFunc("GET /api/v1/ups/{ups}", h.getUPS)
	h.mux.HandleFunc("GET /api/v1/ups/{ups}/vars", h.listVars)
	h.mux.HandleFunc("GET /api/v1/ups/{ups}/vars/{var}", h.getVar)
	h.mux.HandleFunc("PUT /api/v1/ups/{ups}/vars/{var}", h.mutation(h.setVar))
	h.mux.HandleFunc("GET /api/v1/ups/{ups}/commands", h.listCommands)
	h.mux.HandleFunc("POST /api/v1/ups/{ups}/commands/{cmd}", h.mutation(h.runCommand))
	h.mux.HandleFunc("GET /api/v1/ups/{ups}/history/{var}", h.history)
	h.mux.HandleFunc("GET /api/v1/events", h.events)
	h.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no such endpoint: %s %s (see /api/v1/openapi.yaml)", r.Method, r.URL.Path))
	})
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *handler) index(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    "nut-ng",
		"version": h.opts.Version,
		"api":     "v1",
		"docs":    "/api/v1/openapi.yaml",
	})
}

func (h *handler) listUPS(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ups": h.host.ListUPS()})
}

// lookup finds the UPS named in the path or writes a 404.
func (h *handler) lookup(w http.ResponseWriter, r *http.Request) (sdk.UPS, bool) {
	name := r.PathValue("ups")
	u, ok := h.host.GetUPS(name)
	if !ok {
		names := []string{}
		for _, x := range h.host.ListUPS() {
			names = append(names, x.Name)
		}
		writeError(w, http.StatusNotFound, fmt.Sprintf("no UPS named %q%s (configured: %s)",
			name, didYouMean(name, names), strings.Join(names, ", ")))
	}
	return u, ok
}

func (h *handler) getUPS(w http.ResponseWriter, r *http.Request) {
	if u, ok := h.lookup(w, r); ok {
		writeJSON(w, http.StatusOK, u)
	}
}

func (h *handler) listVars(w http.ResponseWriter, r *http.Request) {
	if u, ok := h.lookup(w, r); ok {
		writeJSON(w, http.StatusOK, u.Vars)
	}
}

func (h *handler) getVar(w http.ResponseWriter, r *http.Request) {
	u, ok := h.lookup(w, r)
	if !ok {
		return
	}
	name := r.PathValue("var")
	v, ok := u.Vars[name]
	if !ok {
		names := make([]string, 0, len(u.Vars))
		for k := range u.Vars {
			names = append(names, k)
		}
		writeError(w, http.StatusNotFound, fmt.Sprintf("UPS %s does not report %q%s", u.Name, name, didYouMean(name, names)))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) listCommands(w http.ResponseWriter, r *http.Request) {
	if u, ok := h.lookup(w, r); ok {
		cmds := u.Commands
		if cmds == nil {
			cmds = []sdk.Command{}
		}
		writeJSON(w, http.StatusOK, cmds)
	}
}

func (h *handler) history(w http.ResponseWriter, r *http.Request) {
	u, ok := h.lookup(w, r)
	if !ok {
		return
	}
	name := r.PathValue("var")
	samples, err := h.host.History(u.Name, name)
	if err != nil {
		writeHostError(w, err)
		return
	}
	spec, _ := sdk.LookupVar(name)
	writeJSON(w, http.StatusOK, map[string]any{"ups": u.Name, "var": name, "unit": spec.Unit, "samples": samples})
}

// mutation wraps handlers that change state with the token check.
func (h *handler) mutation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.opts.Trusted {
			if h.opts.Token == "" {
				writeError(w, http.StatusForbidden,
					"changes over the network are disabled; set api.token or api.token_env in nutng.yaml to allow them, or use the nutng CLI on the UPS host")
				return
			}
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(h.opts.Token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="nutng"`)
				writeError(w, http.StatusUnauthorized, "missing or wrong bearer token")
				return
			}
		}
		next(w, r)
	}
}

func (h *handler) runCommand(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Param string `json:"param"`
	}
	if !readBody(w, r, &body) {
		return
	}
	ups, cmd := r.PathValue("ups"), r.PathValue("cmd")
	if err := h.host.RunCommand(r.Context(), ups, cmd, body.Param); err != nil {
		writeHostError(w, err)
		return
	}
	h.opts.Log.Info("API command", "ups", ups, "command", cmd, "remote", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *handler) setVar(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value *string `json:"value"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.Value == nil {
		writeError(w, http.StatusBadRequest, `request body must be {"value": "..."}`)
		return
	}
	ups, name := r.PathValue("ups"), r.PathValue("var")
	if err := h.host.SetVar(r.Context(), ups, name, *body.Value); err != nil {
		writeHostError(w, err)
		return
	}
	h.opts.Log.Info("API variable set", "ups", ups, "variable", name, "value", *body.Value, "remote", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// events streams events as Server-Sent Events. Query parameters:
// ups (only this UPS), recent (replay this many recent notable events
// first), notable=1 (skip "update" events).
func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	q := r.URL.Query()
	only := q.Get("ups")
	if only != "" {
		if _, ok := h.host.GetUPS(only); !ok {
			writeError(w, http.StatusNotFound, fmt.Sprintf("no UPS named %q", only))
			return
		}
	}
	notableOnly := q.Get("notable") == "1" || q.Get("notable") == "true"
	recent, _ := strconv.Atoi(q.Get("recent"))

	ctx := r.Context()
	ch := h.host.Subscribe(ctx, 256)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, ": nutng event stream\n\n")

	send := func(e sdk.Event) bool {
		if only != "" && e.UPS != only {
			return true
		}
		if notableOnly && !e.Type.Notable() {
			return true
		}
		data, _ := json.Marshal(e)
		_, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Type, data)
		return err == nil
	}

	var lastSeq uint64
	if recent > 0 {
		for _, e := range h.host.RecentEvents(recent) {
			if !send(e) {
				return
			}
			lastSeq = e.Seq
		}
	}
	flusher.Flush()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			if e.Seq <= lastSeq { // already replayed
				continue
			}
			if !send(e) {
				return
			}
			flusher.Flush()
		}
	}
}

func readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeHostError maps sdk errors to HTTP status codes.
func writeHostError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, sdk.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, sdk.ErrNotSupported), errors.Is(err, sdk.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, sdk.ErrNotConnected):
		status = http.StatusServiceUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	}
	writeError(w, status, err.Error())
}

// Output runs the API on a TCP address as an sdk.Output.
type Output struct {
	Version string
}

// Info implements sdk.Output.
func (o *Output) Info() sdk.Info {
	return sdk.Info{Name: "rest", Kind: sdk.KindOutput, Version: o.Version, Description: "REST/JSON API with an event stream"}
}

type outputConfig struct {
	Addr  string `yaml:"addr"`
	Token string `yaml:"token"`
}

// ValidateConfig implements sdk.Output.
func (o *Output) ValidateConfig(cfg sdk.Config) error {
	var c outputConfig
	if err := cfg.Decode(&c); err != nil {
		return err
	}
	if c.Addr == "" {
		return errors.New("addr is required")
	}
	return nil
}

// Run implements sdk.Output.
func (o *Output) Run(ctx context.Context, host sdk.Host, cfg sdk.Config, log *slog.Logger) error {
	var c outputConfig
	if err := cfg.Decode(&c); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", c.Addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w (is another nutng or program using that port? change listen.http in nutng.yaml)", c.Addr, err)
	}
	log.Info("REST API listening", "addr", ln.Addr().String(), "changes_allowed", c.Token != "")
	return Serve(ctx, ln, NewHandler(host, Options{Token: c.Token, Version: o.Version, Log: log}))
}

// Serve serves h on ln until ctx is cancelled, then shuts down gracefully.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return nil
}
