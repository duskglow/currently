package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/duskglow/currently/pkg/sdk"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// ups is a simulated UPS: it receives bytes and answers.
type ups interface {
	input(b byte) []byte
	received() []string
}

// fakePort connects a link to a simulated UPS.
type fakePort struct {
	mu  sync.Mutex
	u   ups
	out []byte
}

func (p *fakePort) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range b {
		p.out = append(p.out, p.u.input(c)...)
	}
	return len(b), nil
}

func (p *fakePort) Read(b []byte) (int, error) {
	p.mu.Lock()
	n := copy(b, p.out)
	p.out = p.out[n:]
	p.mu.Unlock()
	if n == 0 {
		time.Sleep(5 * time.Millisecond) // the read timeout
	}
	return n, nil
}

func (p *fakePort) SetReadTimeout(time.Duration) error { return nil }
func (p *fakePort) ResetInputBuffer() error {
	p.mu.Lock()
	p.out = nil
	p.mu.Unlock()
	return nil
}
func (p *fakePort) Close() error { return nil }

// fakeMegatec answers Q1, F and I, records commands, and echoes the
// unsupported ones.
type fakeMegatec struct {
	mu          sync.Mutex
	line        []byte
	status      string
	cmds        []string
	unsupported map[string]bool
}

func newMegatec() *fakeMegatec {
	return &fakeMegatec{status: "(230.1 140.0 229.9 034 50.1 2.05 35.0 00001001", unsupported: map[string]bool{"TL": true}}
}

func (u *fakeMegatec) input(c byte) []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	if c != '\r' {
		u.line = append(u.line, c)
		return nil
	}
	cmd := string(u.line)
	u.line = nil
	switch {
	case cmd == "Q1":
		return []byte(u.status + "\r")
	case cmd == "F":
		return []byte("#230.0 004 24.00 50.0\r")
	case cmd == "I":
		return []byte("#" + "MegaTec Corp   " + " " + "Pro 1000  " + " " + "V1.2      " + "\r") // 15, 10, 10 wide
	case cmd == "" || u.unsupported[cmd]:
		return []byte(cmd + "\r")
	case strings.HasPrefix(cmd, "T") || strings.HasPrefix(cmd, "S") || cmd == "C" || cmd == "CT" || cmd == "Q":
		u.cmds = append(u.cmds, cmd)
		return nil
	}
	return []byte(cmd + "\r") // unknown, such as a stray APC "Y"
}

func (u *fakeMegatec) received() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.cmds...)
}

// fakeAPC answers APC Smart queries.
type fakeAPC struct {
	mu     sync.Mutex
	smart  bool
	q      string
	alert  string // sent before the next reply
	cmds   []string
	at     string // collecting @nnn
	lastKZ map[byte]time.Time
}

func newAPC() *fakeAPC { return &fakeAPC{q: "08", lastKZ: map[byte]time.Time{}} }

func (u *fakeAPC) input(c byte) []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	reply := func(s string) []byte {
		out := u.alert + s + "\r\n"
		u.alert = ""
		return []byte(out)
	}
	if c == 'Y' {
		u.smart = true
		return reply("SM")
	}
	if !u.smart {
		return nil
	}
	if u.at != "" || c == '@' {
		u.at += string(c)
		if len(u.at) == 4 {
			u.cmds = append(u.cmds, u.at)
			u.at = ""
			return reply("OK")
		}
		return nil
	}
	answers := map[byte]string{0x01: "Smart-UPS 1500", 'n': "AS1234567890", 'b': "601.3.D", 'm': "05/14/19",
		'x': "05/14/19", 'g': "024", 'l': "106", 'u': "127", 'p': "020", 'Q': u.q, 'f': "087.0", 'L': "121.5",
		'O': "120.0", 'P': "023.4", 'F': "60.00", 'B': "27.36", 'C': "031.5", 'j': "0045:", 'X': "OK"}
	if a, ok := answers[c]; ok {
		return reply(a)
	}
	switch c {
	case 'W', 'A', 'S', 0x7f:
		u.cmds = append(u.cmds, string(c))
		return reply("OK")
	case 'K', 'Z':
		if t, ok := u.lastKZ[c]; ok && time.Since(t) > 1500*time.Millisecond {
			u.cmds = append(u.cmds, string(c)+string(c))
			delete(u.lastKZ, c)
			return reply("OK")
		}
		u.lastKZ[c] = time.Now()
		return nil
	}
	return reply("NA")
}

func (u *fakeAPC) received() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.cmds...)
}

func openFake(t *testing.T, u ups, cfg sdk.Config) *device {
	t.Helper()
	if cfg["port"] == nil {
		cfg["port"] = "fake"
	}
	if cfg["timeout"] == nil {
		cfg["timeout"] = "200ms"
	}
	d := Driver{open: func(string, int) (port, error) { return &fakePort{u: u}, nil }}
	dev, err := d.Open(context.Background(), cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dev.Close() })
	return dev.(*device)
}

// pollOnce runs one Watch cycle.
func pollOnce(t *testing.T, d *device) map[string]string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var got map[string]string
	_ = d.Watch(ctx, func(u sdk.Update) { got = u.Vars; cancel() })
	if got == nil {
		t.Fatal("no update")
	}
	return got
}

func expect(t *testing.T, vars, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s = %q, want %q", k, vars[k], v)
		}
	}
}

func TestMegatec(t *testing.T) {
	u := newMegatec()
	d := openFake(t, u, sdk.Config{}) // auto: tries APC Smart first
	if d.proto.name() != "megatec" {
		t.Fatalf("protocol %s", d.proto.name())
	}
	expect(t, pollOnce(t, d), map[string]string{
		"ups.status": "OL", "input.voltage": "230.1", "output.voltage": "229.9", "ups.load": "34",
		"input.frequency": "50.1", "ups.temperature": "35", "battery.voltage": "24.6", "battery.charge": "67",
		"battery.voltage.nominal": "24", "input.voltage.nominal": "230", "device.mfr": "MegaTec Corp",
		"device.model": "Pro 1000", "ups.firmware": "V1.2", "ups.type": "offline / line interactive",
		"ups.beeper.status": "enabled",
	})

	u.mu.Lock()
	u.status = "(000.0 000.0 229.9 034 00.0 2.00 35.0 11101000" // on battery, low, boost/AVR
	u.mu.Unlock()
	expect(t, pollOnce(t, d), map[string]string{"ups.status": "OB DISCHRG LB BOOST", "ups.beeper.status": "disabled"})

	run := func(cmd, param string) error { return d.RunCommand(context.Background(), cmd, param) }
	for _, c := range []struct{ cmd, param string }{
		{"shutdown.return", "60"}, {"shutdown.return", "30"}, {"shutdown.stayoff", "600"}, {"shutdown.stop", ""},
		{"test.battery.start.quick", ""}, {"test.battery.start", "5"}, {"test.battery.stop", ""},
		{"beeper.disable", ""}, {"beeper.enable", ""},
	} {
		if err := run(c.cmd, c.param); err != nil {
			t.Fatalf("%s %s: %v", c.cmd, c.param, err)
		}
	}
	want := "S01R0003 S.5R0003 S10R9999 C T T05 CT Q" // beeper.disable: already off
	if got := strings.Join(u.received(), " "); got != want {
		t.Errorf("commands:\n got %s\nwant %s", got, want)
	}
	if err := run("shutdown.return", "45"); !errors.Is(err, sdk.ErrInvalid) {
		t.Errorf("45 s delay: %v", err)
	}
	if err := run("test.battery.start.deep", ""); !errors.Is(err, sdk.ErrNotSupported) {
		t.Errorf("echoed command: %v", err)
	}
}

func TestMegatecChargeOverride(t *testing.T) {
	d := openFake(t, newMegatec(), sdk.Config{"protocol": "megatec", "battery_voltage_low": 22.0, "battery_voltage_high": 26.0})
	expect(t, pollOnce(t, d), map[string]string{"battery.charge": "65"}) // 24.6 V
}

func TestAPCSmart(t *testing.T) {
	u := newAPC()
	d := openFake(t, u, sdk.Config{})
	if d.proto.name() != "apcsmart" {
		t.Fatalf("protocol %s", d.proto.name())
	}
	expect(t, pollOnce(t, d), map[string]string{
		"ups.status": "OL", "device.mfr": "APC", "device.model": "Smart-UPS 1500", "device.serial": "AS1234567890",
		"ups.firmware": "601.3.D", "battery.charge": "87", "battery.runtime": "2700", "input.voltage": "121.5",
		"ups.load": "23.4", "battery.voltage": "27.36", "ups.temperature": "31.5", "ups.delay.shutdown": "20",
		"input.transfer.low": "106", "ups.test.result": "Passed",
	})

	run := func(cmd, param string) error { return d.RunCommand(context.Background(), cmd, param) }
	if err := run("shutdown.return", "20"); err != nil { // on line: @000
		t.Fatal(err)
	}
	u.mu.Lock()
	u.q, u.alert = "50", "!" // on battery, low battery, with a line-fail alert
	u.mu.Unlock()
	expect(t, pollOnce(t, d), map[string]string{"ups.status": "OB LB DISCHRG"})
	for _, c := range []string{"test.battery.start", "shutdown.return", "shutdown.stop", "shutdown.stayoff"} {
		if err := run(c, "20"); err != nil {
			t.Fatalf("%s: %v", c, err)
		}
	}
	want := "@000 W S \x7f KK"
	if got := strings.Join(u.received(), " "); got != want {
		t.Errorf("commands:\n got %q\nwant %q", got, want)
	}
	if err := run("shutdown.return", "60"); !errors.Is(err, sdk.ErrInvalid) || !strings.Contains(err.Error(), "grace delay of 20") {
		t.Errorf("60 s with a 20 s grace delay: %v", err)
	}
}

func TestNoUPS(t *testing.T) {
	d := Driver{open: func(string, int) (port, error) { return &fakePort{u: silent{}}, nil }}
	_, err := d.Open(context.Background(), sdk.Config{"port": "COM9", "timeout": "100ms"}, quiet)
	if err == nil || !strings.Contains(err.Error(), "no answer from an APC Smart or Megatec UPS on COM9") {
		t.Errorf("error %v", err)
	}
}

type silent struct{}

func (silent) input(byte) []byte  { return nil }
func (silent) received() []string { return nil }

func TestValidate(t *testing.T) {
	for want, cfg := range map[string]sdk.Config{
		"port is required":           {},
		`protocol "contact"`:         {"port": "x", "protocol": "contact"},
		"set both battery_voltage":   {"port": "x", "battery_voltage_low": 20.0},
		`unknown option "buad"`:      {"port": "x", "buad": 9600},
		"interval must be at least ": {"port": "x", "interval": "100ms"},
	} {
		if err := (Driver{}).ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: %v, want %q", cfg, err, want)
		}
	}
	if err := (Driver{}).ValidateConfig(sdk.Config{"port": "/dev/ttyUSB0", "protocol": "Q1"}); err != nil {
		t.Error(err)
	}
}

// TestRealPort talks to a simulated UPS over a real serial device pair,
// such as two ends of `socat pty,raw,echo=0,link=/tmp/ttyUPS
// pty,raw,echo=0,link=/tmp/ttyFAKE`: CURRENTLY_TEST_SERIAL=/tmp/ttyUPS,/tmp/ttyFAKE.
func TestRealPort(t *testing.T) {
	ports := os.Getenv("CURRENTLY_TEST_SERIAL")
	if ports == "" {
		t.Skip("set CURRENTLY_TEST_SERIAL=driver-port,device-port")
	}
	driverPort, devicePort, _ := strings.Cut(ports, ",")
	for name, u := range map[string]ups{"megatec": newMegatec(), "apcsmart": newAPC()} {
		t.Run(name, func(t *testing.T) {
			f, err := os.OpenFile(devicePort, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				buf := make([]byte, 64)
				for {
					n, err := f.Read(buf)
					if err != nil {
						return
					}
					for _, c := range buf[:n] {
						if out := u.input(c); len(out) > 0 {
							_, _ = f.Write(out)
						}
					}
				}
			}()
			defer func() { f.Close(); <-done }()
			dev, err := Driver{}.Open(context.Background(), sdk.Config{"port": driverPort}, quiet)
			if err != nil {
				t.Fatal(err)
			}
			defer dev.Close()
			d := dev.(*device)
			if d.proto.name() != name {
				t.Fatalf("detected %s", d.proto.name())
			}
			vars := pollOnce(t, d)
			if vars["ups.status"] != "OL" || vars["device.model"] == "" {
				t.Errorf("vars %v", vars)
			}
			if err := d.RunCommand(context.Background(), "shutdown.stop", ""); err != nil {
				t.Errorf("shutdown.stop: %v", err)
			}
		})
	}
}
