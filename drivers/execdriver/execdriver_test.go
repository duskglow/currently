package execdriver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/duskglow/currently/pkg/sdk"
)

// The test binary doubles as the external program: with CURRENTLY_HELPER set,
// TestHelper prints what the mode asks for and exits.
func TestHelper(t *testing.T) {
	mode := os.Getenv("CURRENTLY_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "json":
		fmt.Println(`{"ups.status": "OL", "battery.charge": 87, "ups.beeper": true}`)
	case "upsc":
		fmt.Print("battery.charge: 55\nups.status: OB DISCHRG\ndevice.model: Back-UPS 1500\n")
	case "stream":
		fmt.Println(`{"ups.status": "OL", "battery.charge": 100}`)
		fmt.Print("ups.status: OB\nbattery.charge: 99\n\n")
		time.Sleep(200 * time.Millisecond)
	case "fail":
		fmt.Fprintln(os.Stderr, "cannot reach the UPS")
		os.Exit(2)
	case "command":
		fmt.Println("param=" + os.Getenv("CURRENTLY_PARAM"))
	}
	os.Exit(0)
}

func helper(t *testing.T, mode string) []any {
	t.Setenv("CURRENTLY_HELPER", mode)
	return []any{os.Args[0], "-test.run=^TestHelper$"}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func open(t *testing.T, cfg sdk.Config) sdk.Device {
	t.Helper()
	dev, err := New().Open(context.Background(), cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return dev
}

func firstUpdate(t *testing.T, dev sdk.Device, n int) []sdk.Update {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got []sdk.Update
	_ = dev.Watch(ctx, func(u sdk.Update) {
		got = append(got, u)
		if len(got) >= n {
			cancel()
		}
	})
	return got
}

func TestPollJSONAndUpscFormats(t *testing.T) {
	dev := open(t, sdk.Config{"command": helper(t, "json")})
	u := firstUpdate(t, dev, 1)
	if v := u[0].Vars; v["ups.status"] != "OL" || v["battery.charge"] != "87" || v["ups.beeper"] != "true" {
		t.Errorf("json vars %v", v)
	}
	dev = open(t, sdk.Config{"command": helper(t, "upsc")})
	u = firstUpdate(t, dev, 1)
	if v := u[0].Vars; v["ups.status"] != "OB DISCHRG" || v["device.model"] != "Back-UPS 1500" {
		t.Errorf("upsc vars %v", v)
	}
}

func TestStream(t *testing.T) {
	dev := open(t, sdk.Config{"command": helper(t, "stream"), "mode": "stream"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var got []sdk.Update
	err := dev.Watch(ctx, func(u sdk.Update) { got = append(got, u) })
	if len(got) != 2 || got[0].Vars["ups.status"] != "OL" || got[1].Vars["ups.status"] != "OB" {
		t.Errorf("updates %+v", got)
	}
	if err == nil || ctx.Err() != nil {
		t.Errorf("a stream that ends must return an error so it restarts: %v", err)
	}
}

func TestPollFailureKeepsTrying(t *testing.T) {
	dev := open(t, sdk.Config{"command": helper(t, "fail"), "interval": "500ms"})
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	n := 0
	err := dev.Watch(ctx, func(sdk.Update) { n++ })
	if n != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("updates %d, err %v", n, err)
	}
}

func TestCommands(t *testing.T) {
	args := helper(t, "command")
	dev := open(t, sdk.Config{"command": args, "commands": map[string]any{"beeper.disable": args}})
	if cmds := dev.Commands(); len(cmds) != 1 || cmds[0].Name != "beeper.disable" {
		t.Errorf("commands %v", cmds)
	}
	if err := dev.RunCommand(context.Background(), "beeper.disable", "x"); err != nil {
		t.Error(err)
	}
	if err := dev.RunCommand(context.Background(), "load.off", ""); !errors.Is(err, sdk.ErrNotSupported) {
		t.Errorf("unknown command: %v", err)
	}
}

func TestOptions(t *testing.T) {
	d := New()
	if err := d.ValidateConfig(sdk.Config{"command": "upsc ups@nas"}); err != nil {
		t.Error(err)
	}
	for _, cfg := range []sdk.Config{{}, {"command": "x", "mode": "push"}, {"command": "x", "interval": "1ms"}, {"commnd": "x"}} {
		if err := d.ValidateConfig(cfg); err == nil {
			t.Errorf("%v: no error", cfg)
		}
	}
	if _, err := d.Open(context.Background(), sdk.Config{"command": []any{"surely-no-such-program-currently"}}, quiet); err == nil {
		t.Error("missing program accepted")
	}
}

func TestParseLine(t *testing.T) {
	vars := map[string]string{}
	for _, bad := range []string{`{"a": [1]}`, "no colon here", `{broken`} {
		if err := parseLine(bad, vars); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := parseLine("# comment", vars); err != nil || len(vars) != 0 {
		t.Errorf("comment: %v %v", err, vars)
	}
}
