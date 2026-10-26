package scaffold

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
	"github.com/nut-ng/nut-ng/internal/plugins"
	"github.com/nut-ng/nut-ng/internal/registry"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func repoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestCreateValidates(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []Params{{Kind: "thing", Name: "x", Lang: "go"}, {Kind: "driver", Name: "Bad Name", Lang: "go"}, {Kind: "driver", Name: "x", Lang: "rust"}} {
		if _, err := Create(filepath.Join(dir, "a"), p); err == nil {
			t.Errorf("%+v accepted", p)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(dir, Params{Kind: "driver", Name: "x", Lang: "go"}); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("non-empty dir: %v", err)
	}
}

// buildGo scaffolds a Go plugin, builds it against this checkout, and
// returns the directory holding the executable.
func buildGo(t *testing.T, kind, name string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a plugin")
	}
	src := filepath.Join(t.TempDir(), "src")
	if _, err := Create(src, Params{Kind: kind, Name: name, Lang: "go", SDKPath: repoRoot(t)}); err != nil {
		t.Fatal(err)
	}
	gobin := filepath.Join(runtime.GOROOT(), "bin", "go") //nolint:staticcheck // the go running the tests, even when it is not on PATH
	bin := t.TempDir()
	exe := "nutng-" + kind + "-" + name
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	for _, args := range [][]string{{"mod", "tidy"}, {"build", "-o", filepath.Join(bin, exe), "."}} {
		cmd := exec.Command(gobin, args...)
		cmd.Dir = src
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return bin
}

func TestGoDriverScaffoldRuns(t *testing.T) {
	bin := buildGo(t, "driver", "scaffolded")
	reg := registry.Builtin()
	m := plugins.Load(context.Background(), plugins.Options{Dirs: []string{bin}}, reg, quiet)
	defer m.Close()
	d, ok := reg.Driver("scaffolded")
	if !ok {
		t.Fatalf("not loaded: %+v", m.Statuses())
	}
	if err := d.ValidateConfig(sdk.Config{}); err == nil || !strings.Contains(err.Error(), "address is required") {
		t.Errorf("validate: %v", err)
	}
	checkDevice(t, d, sdk.Config{"address": "x", "interval": "100ms"}, false)
}

func TestGoOutputScaffoldRuns(t *testing.T) {
	bin := buildGo(t, "output", "scaffoldout")
	reg := registry.Builtin()
	m := plugins.Load(context.Background(), plugins.Options{Dirs: []string{bin}}, reg, quiet)
	defer m.Close()
	out, ok := reg.Output("scaffoldout")
	if !ok {
		t.Fatalf("not loaded: %+v", m.Statuses())
	}
	if err := out.ValidateConfig(sdk.Config{}); err == nil {
		t.Error("missing target accepted")
	}
	c := core.New(quiet)
	_ = c.AddUPS(core.UPSSpec{Name: "sim", Driver: simulator.New(), Config: sdk.Config{"interval": "200ms"}})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	if err := out.Run(ctx, plugins.ReadOnlyHost{Host: c}, sdk.Config{"target": "x"}, quiet); err != nil {
		t.Errorf("Run: %v", err)
	}
}

// checkDevice opens a device, reads it, and runs a command.
func checkDevice(t *testing.T, d sdk.Driver, cfg sdk.Config, setVar bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dev, err := d.Open(ctx, cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	got := make(chan sdk.Update, 4)
	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()
	go func() { _ = dev.Watch(wctx, func(u sdk.Update) { got <- u }) }()
	select {
	case u := <-got:
		if u.Vars["ups.status"] != "OL" || u.Vars["battery.charge"] != "100" {
			t.Errorf("vars %v", u.Vars)
		}
	case <-ctx.Done():
		t.Fatal("no update from the plugin")
	}
	if len(dev.Commands()) != 1 {
		t.Errorf("commands %v", dev.Commands())
	}
	if err := dev.RunCommand(ctx, "beeper.disable", ""); setVar && err != nil {
		t.Errorf("command: %v", err)
	}
	if setVar {
		if err := dev.SetVar(ctx, "battery.charge", "lots"); !errors.Is(err, sdk.ErrInvalid) {
			t.Errorf("invalid value: %v", err)
		}
		if err := dev.SetVar(ctx, "battery.charge", "42"); err != nil {
			t.Errorf("set: %v", err)
		}
		if err := dev.RunCommand(ctx, "load.off", ""); !errors.Is(err, sdk.ErrNotSupported) {
			t.Errorf("unsupported command: %v", err)
		}
	}
}

// TestPythonScaffoldRuns needs a Python with the packages in
// requirements.txt: NUTNG_TEST_PYTHON=/path/to/python go test ./internal/scaffold
func TestPythonScaffoldRuns(t *testing.T) {
	py := os.Getenv("NUTNG_TEST_PYTHON")
	if py == "" {
		t.Skip("set NUTNG_TEST_PYTHON to a Python with grpcio, grpcio-health-checking and protobuf")
	}
	dir := filepath.Join(t.TempDir(), "py")
	if _, err := Create(dir, Params{Kind: "driver", Name: "pyscaffold", Lang: "python", Python: py}); err != nil {
		t.Fatal(err)
	}
	reg := registry.Builtin()
	m := plugins.Load(context.Background(), plugins.Options{Commands: [][]string{{py, filepath.Join(dir, "plugin.py")}}}, reg, quiet)
	defer m.Close()
	d, ok := reg.Driver("pyscaffold")
	if !ok {
		t.Fatalf("not loaded: %+v", m.Statuses())
	}
	if err := d.ValidateConfig(sdk.Config{"adress": "x"}); err == nil || !strings.Contains(err.Error(), "address is required") {
		t.Errorf("validate: %v", err)
	}
	checkDevice(t, d, sdk.Config{"address": "x", "interval": 0.1}, true)
}
