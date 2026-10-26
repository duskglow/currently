package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/duskglow/currently/internal/client"
	"github.com/duskglow/currently/internal/config"
	"github.com/duskglow/currently/internal/registry"
)

// recorder is a policy.Executor that only records; nothing is run.
type recorder struct {
	mu        sync.Mutex
	shutdowns []time.Time
	hooks     []string
}

func (r *recorder) RunHook(_ context.Context, command string, _ []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = append(r.hooks, command)
	return nil
}

func (r *recorder) Shutdown(context.Context, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shutdowns = append(r.shutdowns, time.Now())
	return nil
}

func (r *recorder) shutdownAt() (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.shutdowns) == 0 {
		return time.Time{}, false
	}
	return r.shutdowns[0], true
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func start(t *testing.T, yaml string, exec *recorder) {
	t.Helper()
	cfg, err := config.Parse("test.yaml", []byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(registry.Builtin()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- Run(ctx, Options{Config: cfg, Registry: registry.Builtin(), Version: "test",
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Executor: exec,
			Ready: func(string) { close(ready) }})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("daemon exited: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not start")
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Logf("daemon: %v", err)
		}
	})
}

// TestPrimaryAndSecondaryShutdown runs a primary Currently with a simulated
// UPS and a secondary Currently that follows it over the NUT protocol. A
// simulated outage must shut the secondary down first; the primary waits
// for it to log out, then shuts down. Executors only record.
func TestPrimaryAndSecondaryShutdown(t *testing.T) {
	dir := t.TempDir()
	nutAddr := freePort(t)
	primarySock := filepath.Join(dir, "p.sock")
	primary, secondary := &recorder{}, &recorder{}

	start(t, fmt.Sprintf(`
ups:
  sim:
    driver: simulator
    interval: 200ms
    speed: 300
    runtime: 10m
listen:
  http: off
  metrics: off
  nut: %s
  control: %s
users:
  mon: {password: pw, role: secondary}
shutdown:
  wait_for_secondaries: 10s
  final_delay: 10ms
  hooks: ["primary-hook"]
  command: "recorded only"
`, nutAddr, primarySock), primary)

	start(t, fmt.Sprintf(`
ups:
  office:
    driver: nut
    host: %s
    ups: sim
    username: mon
    password: pw
    interval: 500ms
listen:
  http: off
  metrics: off
  nut: off
  control: %s
shutdown:
  final_delay: 10ms
  command: "recorded only"
`, nutAddr, filepath.Join(dir, "s.sock")), secondary)

	pc := client.NewSocket(primarySock)
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := pc.Shutdown(ctx)
		if err == nil && st.State == "armed" && len(st.Plan) > 0 && strings.Contains(st.Plan[0], "logged in now: 127.0.0.1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("secondary never logged in: %+v %v", st, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := pc.RunCommand(ctx, "sim", "sim.power.fail", ""); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		_, s := secondary.shutdownAt()
		_, p := primary.shutdownAt()
		if s && p {
			break
		}
		if time.Now().After(deadline) {
			st, _ := pc.Shutdown(ctx)
			t.Fatalf("shutdowns: secondary %v, primary %v; primary status %+v", s, p, st)
		}
		time.Sleep(50 * time.Millisecond)
	}
	sAt, _ := secondary.shutdownAt()
	pAt, _ := primary.shutdownAt()
	if !sAt.Before(pAt) {
		t.Errorf("primary shut down at %s, before the secondary at %s", pAt, sAt)
	}

	st, err := pc.Shutdown(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, s := range st.Steps {
		steps = append(steps, s.Action+" => "+s.Result)
	}
	got := strings.Join(steps, "\n")
	for _, want := range []string{
		"Set FSD on sim, telling its NUT clients to shut down => done",
		"Wait up to 10s for NUT clients to log out => done",
		"Run hook: primary-hook => done",
		"Shut down this computer: recorded only => done",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("primary steps lack %q:\n%s", want, got)
		}
	}
	if !strings.Contains(st.Reason, "battery is low") {
		t.Errorf("reason %q", st.Reason)
	}
}
