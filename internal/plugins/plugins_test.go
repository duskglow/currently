package plugins

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nut-ng/nut-ng/drivers/simulator"
	"github.com/nut-ng/nut-ng/internal/core"
	"github.com/nut-ng/nut-ng/internal/registry"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// pluginDir holds the test plugins, built once by TestMain.
var pluginDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "nutng-plugins")
	if err != nil {
		panic(err)
	}
	pluginDir = dir
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	gobin := filepath.Join(runtime.GOROOT(), "bin", "go") //nolint:staticcheck // the go running the tests, even when it is not on PATH
	for kind, name := range map[string]string{"driver": "nutng-driver-testdriver", "output": "nutng-output-testoutput"} {
		cmd := exec.Command(gobin, "build", "-o", filepath.Join(dir, name+exe), "./testdata/"+kind)
		if out, err := cmd.CombinedOutput(); err != nil {
			os.Stderr.Write(out)
			panic(err)
		}
	}
	// Files that must be ignored: wrong name, and a directory.
	_ = os.WriteFile(filepath.Join(dir, "README.txt"), []byte("not a plugin"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "nutng-driver-dir"+exe), 0o755)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func load(t *testing.T, opts Options) (*Manager, *registry.Registry) {
	t.Helper()
	reg := registry.Builtin()
	m := Load(context.Background(), opts, reg, quiet)
	t.Cleanup(m.Close)
	return m, reg
}

func TestFindAndDescribe(t *testing.T) {
	found := Find([]string{filepath.Join(pluginDir, "missing"), pluginDir})
	if len(found) != 2 {
		t.Fatalf("found %v", found)
	}
	m, reg := load(t, Options{Dirs: []string{pluginDir}})
	st := m.Statuses()
	if len(st) != 2 {
		t.Fatalf("statuses %+v", st)
	}
	for _, s := range st {
		if s.Error != "" || !s.Running || s.Starts != 1 {
			t.Errorf("status %+v", s)
		}
	}
	d, ok := reg.Driver("testdriver")
	if !ok || d.Info().Version != "1.2.3" || d.Info().Kind != sdk.KindDriver {
		t.Fatalf("driver %+v", d)
	}
	if _, ok := reg.Output("testoutput"); !ok {
		t.Fatal("output not registered")
	}
	disc, err := d.Discover(context.Background())
	if err != nil || len(disc) != 1 || disc[0].Driver != "testdriver" || disc[0].Config["serial"] != "T1" {
		t.Errorf("discover %+v %v", disc, err)
	}
	if err := d.ValidateConfig(sdk.Config{"serail": "x"}); err == nil || !strings.Contains(err.Error(), `did you mean "serial"`) {
		t.Errorf("validate: %v", err)
	}
}

func TestBrokenAndDuplicatePlugins(t *testing.T) {
	m, _ := load(t, Options{Commands: [][]string{
		{filepath.Join(pluginDir, "no-such-plugin")},
		{os.Args[0], "-test.run=TestNothing"}, // a program that is not a plugin
	}})
	for _, s := range m.Statuses() {
		if s.Error == "" {
			t.Errorf("broken plugin reported no error: %+v", s)
		}
	}
	// A plugin may not take a name that is already used.
	reg := registry.Builtin()
	first := Load(context.Background(), Options{Dirs: []string{pluginDir}}, reg, quiet)
	defer first.Close()
	second := Load(context.Background(), Options{Dirs: []string{pluginDir}}, reg, quiet)
	defer second.Close()
	for _, s := range second.Statuses() {
		if !strings.Contains(s.Error, "already has this name") {
			t.Errorf("duplicate accepted: %+v", s)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runCore runs a core with the given UPSes.
func runCore(t *testing.T, specs ...core.UPSSpec) *core.Core {
	t.Helper()
	c := core.New(quiet)
	c.MinBackoff, c.MaxBackoff = 50*time.Millisecond, 200*time.Millisecond
	for _, s := range specs {
		if err := c.AddUPS(s); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return c
}

func TestDriverPluginAndRestart(t *testing.T) {
	m, reg := load(t, Options{Dirs: []string{pluginDir}})
	d, _ := reg.Driver("testdriver")
	c := runCore(t, core.UPSSpec{Name: "plug", Driver: d, Config: sdk.Config{"serial": "P1", "crash_after": "1s"}})
	waitFor(t, "the plugin UPS", func() bool { u, _ := c.GetUPS("plug"); return u.Value("device.serial") == "P1" })
	u, _ := c.GetUPS("plug")
	firstPID := u.Value("driver.pid")

	ctx := context.Background()
	if err := c.RunCommand(ctx, "plug", "beeper.disable", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.SetVar(ctx, "plug", "battery.charge", "lots"); !errors.Is(err, sdk.ErrInvalid) {
		t.Errorf("invalid value crossed the wire as %v", err)
	}

	// The plugin exits after a second; nutng must start it again.
	waitFor(t, "a restarted plugin", func() bool {
		u, _ := c.GetUPS("plug")
		pid := u.Value("driver.pid")
		return pid != "" && pid != firstPID
	})
	if st := m.Statuses()[0]; st.Starts < 2 {
		t.Errorf("starts = %d after a crash", st.Starts)
	}
}

func TestOutputPluginWithACL(t *testing.T) {
	_, reg := load(t, Options{Dirs: []string{pluginDir}})
	out, _ := reg.Output("testoutput")
	c := runCore(t, core.UPSSpec{Name: "rack", Driver: simulator.New(), Config: sdk.Config{"interval": "200ms"}})
	waitFor(t, "the simulator", func() bool { u, _ := c.GetUPS("rack"); return u.State == sdk.StateConnected })

	run := func(host sdk.Host) string {
		t.Helper()
		file := filepath.Join(t.TempDir(), "out.txt")
		if err := out.ValidateConfig(sdk.Config{}); err == nil || !strings.Contains(err.Error(), "file is required") {
			t.Errorf("validate: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- out.Run(ctx, host, sdk.Config{"file": file}, quiet) }()
		waitFor(t, "events in the output file", func() bool {
			b, _ := os.ReadFile(file)
			return strings.Contains(string(b), "event update rack")
		})
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
		b, _ := os.ReadFile(file)
		return string(b)
	}

	got := run(ReadOnlyHost{c})
	if !strings.Contains(got, "ups rack OL") || !strings.Contains(got, "may not control UPSes") {
		t.Errorf("read-only output saw:\n%s", got)
	}
	got = run(c)
	if !strings.Contains(got, "command error: <nil>") {
		t.Errorf("trusted output saw:\n%s", got)
	}
}

func TestHostServerNeedsToken(t *testing.T) {
	hs, err := startHostServer(runCore(t), true, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer hs.stop()
	if err := hs.check(context.Background()); err == nil {
		t.Error("call without a token accepted")
	}
}

func TestStopUnusedRestartsOnDemand(t *testing.T) {
	m, reg := load(t, Options{Dirs: []string{pluginDir}})
	m.StopUnused(map[string]bool{"testdriver": true})
	for _, s := range m.Statuses() {
		if want := s.Name == "testdriver"; s.Running != want {
			t.Errorf("%s running = %v, want %v", s.Name, s.Running, want)
		}
	}
	// An unused plugin starts again when something needs it.
	out, _ := reg.Output("testoutput")
	if err := out.ValidateConfig(sdk.Config{"file": "x"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range m.Statuses() {
		if s.Name == "testoutput" && (!s.Running || s.Starts != 2) {
			t.Errorf("testoutput after use: %+v", s)
		}
	}
}

func TestNothing(t *testing.T) {}
