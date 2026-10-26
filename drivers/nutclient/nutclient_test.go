package nutclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/duskglow/currently/drivers/simulator"
	"github.com/duskglow/currently/internal/core"
	"github.com/duskglow/currently/outputs/nutproto"
	"github.com/duskglow/currently/pkg/sdk"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type deadDriver struct{}

func (deadDriver) Info() sdk.Info                                     { return sdk.Info{Name: "dead"} }
func (deadDriver) Discover(context.Context) ([]sdk.Discovered, error) { return nil, nil }
func (deadDriver) ValidateConfig(sdk.Config) error                    { return nil }
func (deadDriver) Open(context.Context, sdk.Config, *slog.Logger) (sdk.Device, error) {
	return nil, io.ErrUnexpectedEOF
}

// server runs a Currently core with UPS "sim" (and "dead", which never
// connects unless onlySim) behind a NUT protocol server.
func server(t *testing.T, onlySim bool) (addr string, host *core.Core, srv *nutproto.Server) {
	t.Helper()
	host = core.New(quiet)
	if err := host.AddUPS(core.UPSSpec{Name: "sim", Driver: simulator.New(), Config: sdk.Config{"interval": "200ms"}}); err != nil {
		t.Fatal(err)
	}
	if !onlySim {
		if err := host.AddUPS(core.UPSSpec{Name: "dead", Driver: deadDriver{}}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	go func() { _ = host.Run(ctx); done <- struct{}{} }()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv = nutproto.New(host, nutproto.Options{Log: quiet, Users: map[string]nutproto.User{
		"mon":   {Password: "pw", Role: nutproto.RoleSecondary},
		"admin": {Password: "pw", Role: nutproto.RoleAdmin},
	}})
	go func() { _ = srv.Serve(ctx, ln); done <- struct{}{} }()
	t.Cleanup(func() { cancel(); <-done; <-done })
	waitFor(t, "sim to connect", func() bool { u, _ := host.GetUPS("sim"); return u.State == sdk.StateConnected })
	return ln.Addr().String(), host, srv
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func open(t *testing.T, cfg sdk.Config) (sdk.Device, error) {
	t.Helper()
	if _, ok := cfg["interval"]; !ok {
		cfg["interval"] = "500ms"
	}
	return New().Open(context.Background(), cfg, quiet)
}

func TestFollowsServerAndCountsAsLogin(t *testing.T) {
	addr, host, srv := server(t, false)
	dev, err := open(t, sdk.Config{"host": addr, "ups": "sim", "username": "mon", "password": "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(srv.Clients("sim")); got != 1 {
		t.Errorf("logged-in clients = %d, want 1", got)
	}
	updates := make(chan sdk.Update, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = dev.Watch(ctx, func(u sdk.Update) { updates <- u }) }()
	u := <-updates
	if u.Vars["ups.status"] != "OL" || u.Vars["battery.charge"] != "100" {
		t.Fatalf("vars = %v", u.Vars)
	}
	if len(dev.Commands()) == 0 || len(dev.Writable()) == 0 {
		t.Errorf("commands %v, writable %v", dev.Commands(), dev.Writable())
	}

	// FSD set on the primary reaches the secondary.
	if err := host.SetForcedShutdown(ctx, "sim"); err != nil {
		t.Fatal(err)
	}
	for u = range updates {
		if strings.Contains(u.Vars["ups.status"], "FSD") {
			break
		}
	}

	// A secondary may not run commands.
	if err := dev.RunCommand(ctx, "beeper.disable", ""); !errors.Is(err, sdk.ErrNotSupported) || !strings.Contains(err.Error(), "admin rights") {
		t.Errorf("command as secondary: %v", err)
	}

	// Closing logs out, which the primary waits for.
	dev.Close()
	waitFor(t, "logout", func() bool { return len(srv.Clients("sim")) == 0 })
}

func TestAdminCommandsAndSingleUPS(t *testing.T) {
	addr, host, _ := server(t, true)
	// With one UPS on the server, `ups:` may be left out.
	dev, err := open(t, sdk.Config{"host": addr, "username": "admin", "password": "pw"})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	ctx := context.Background()
	if err := dev.RunCommand(ctx, "sim.power.fail", ""); err != nil {
		t.Fatal(err)
	}
	if err := dev.SetVar(ctx, "ups.load", "70"); err != nil {
		t.Fatal(err)
	}
	if err := dev.SetVar(ctx, "ups.load", "lots"); !errors.Is(err, sdk.ErrInvalid) {
		t.Errorf("bad value: %v", err)
	}
	if err := dev.RunCommand(ctx, "load.off", ""); !errors.Is(err, sdk.ErrNotSupported) {
		t.Errorf("unsupported command: %v", err)
	}
	waitFor(t, "the outage", func() bool {
		u, _ := host.GetUPS("sim")
		return u.Status().Has(sdk.FlagOnBattery) && u.Value("ups.load") == "70"
	})
}

func TestStaleServerDataReportsNothing(t *testing.T) {
	addr, _, _ := server(t, false)
	dev, err := open(t, sdk.Config{"host": addr, "ups": "dead"})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	got := 0
	err = dev.Watch(ctx, func(sdk.Update) { got++ })
	if got != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("updates %d, err %v; want none until the deadline", got, err)
	}
}

func TestOpenErrors(t *testing.T) {
	addr, _, _ := server(t, false)
	tests := []struct {
		cfg  sdk.Config
		want string
	}{
		{sdk.Config{"host": addr}, "has 2 UPSes (sim, dead); set `ups:`"},
		{sdk.Config{"host": addr, "ups": "smi"}, `no UPS named "smi" (did you mean "sim"?)`},
		{sdk.Config{"host": addr, "ups": "sim", "username": "mon", "password": "wrong"}, "wrong password"},
		{sdk.Config{"host": "127.0.0.1:1"}, "connect to NUT server 127.0.0.1:1"},
	}
	for _, tt := range tests {
		_, err := open(t, tt.cfg)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: got %v, want %q", tt.cfg, err, tt.want)
		}
	}
}

func TestValidateConfig(t *testing.T) {
	d := New()
	if err := d.ValidateConfig(sdk.Config{"host": "nas.local", "ups": "rack", "username": "u", "password": "p"}); err != nil {
		t.Error(err)
	}
	for _, cfg := range []sdk.Config{{}, {"host": "h", "username": "u"}, {"host": "h", "password": "a", "password_env": "B"}, {"host": "h", "interval": "1ms"}} {
		if err := d.ValidateConfig(cfg); err == nil {
			t.Errorf("%v: no error", cfg)
		}
	}
	if got := (Options{Host: "nas.local"}).addr(); got != "nas.local:3493" {
		t.Errorf("default port: %s", got)
	}
	if got := (Options{Host: "[::1]"}).addr(); got != "[::1]:3493" {
		t.Errorf("IPv6: %s", got)
	}
}
