package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/duskglow/currently/drivers/simulator"
	"github.com/duskglow/currently/internal/core"
	"github.com/duskglow/currently/outputs/snmp"
	"github.com/duskglow/currently/pkg/sdk"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeAgent is a v1/v2c SNMP agent over a fixed table, built with
// gosnmp's packet encoding. It records SETs made with the write community.
type fakeAgent struct {
	mu        sync.Mutex
	vals      map[string]gosnmp.SnmpPDU
	sets      []gosnmp.SnmpPDU
	community string
	write     string
}

func newFake(objects ...gosnmp.SnmpPDU) *fakeAgent {
	f := &fakeAgent{vals: map[string]gosnmp.SnmpPDU{}, community: "public", write: "private"}
	for _, o := range objects {
		f.vals[o.Name] = o
	}
	return f
}

func str(oid, s string) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.OctetString, Value: []byte(s)}
}
func num(oid string, n int) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Integer, Value: n}
}
func gauge(oid string, n uint) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.Gauge32, Value: n}
}
func ticks(oid string, n uint32) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid, Type: gosnmp.TimeTicks, Value: n}
}
func oid(name, v string) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: name, Type: gosnmp.ObjectIdentifier, Value: v}
}

func (f *fakeAgent) sorted() []string {
	keys := make([]string, 0, len(f.vals))
	for k := range f.vals {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return compareOID(a, b) })
	return keys
}

func compareOID(a, b string) int {
	pa, pb := strings.Split(strings.Trim(a, "."), "."), strings.Split(strings.Trim(b, "."), ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		var x, y int
		for _, c := range pa[i] {
			x = x*10 + int(c-'0')
		}
		for _, c := range pb[i] {
			y = y*10 + int(c-'0')
		}
		if x != y {
			return x - y
		}
	}
	return len(pa) - len(pb)
}

func (f *fakeAgent) serve(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if resp := f.handle(buf[:n]); resp != nil {
				_, _ = pc.WriteTo(resp, from)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func (f *fakeAgent) handle(pkt []byte) []byte {
	req, err := (&gosnmp.GoSNMP{}).SnmpDecodePacket(pkt)
	if err != nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Community != f.community && req.Community != f.write {
		return nil
	}
	v1 := req.Version == gosnmp.Version1
	resp := &gosnmp.SnmpPacket{Version: req.Version, Community: req.Community, PDUType: gosnmp.GetResponse,
		RequestID: req.RequestID}
	keys := f.sorted()
	next := func(name string) (gosnmp.SnmpPDU, bool) {
		for _, k := range keys {
			if compareOID(k, name) > 0 {
				return f.vals[k], true
			}
		}
		return gosnmp.SnmpPDU{}, false
	}
	switch req.PDUType {
	case gosnmp.GetRequest:
		for i, v := range req.Variables {
			got, ok := f.vals[v.Name]
			if !ok {
				if v1 {
					resp.Error, resp.ErrorIndex, resp.Variables = gosnmp.NoSuchName, uint8(i+1), req.Variables
					break
				}
				got = gosnmp.SnmpPDU{Name: v.Name, Type: gosnmp.NoSuchObject}
			}
			resp.Variables = append(resp.Variables, got)
		}
	case gosnmp.GetNextRequest:
		for i, v := range req.Variables {
			got, ok := next(v.Name)
			if !ok {
				if v1 {
					resp.Error, resp.ErrorIndex, resp.Variables = gosnmp.NoSuchName, uint8(i+1), req.Variables
					break
				}
				got = gosnmp.SnmpPDU{Name: v.Name, Type: gosnmp.EndOfMibView}
			}
			resp.Variables = append(resp.Variables, got)
		}
	case gosnmp.GetBulkRequest:
		cur := req.Variables[0].Name
		for range max(req.MaxRepetitions, 1) {
			got, ok := next(cur)
			if !ok {
				resp.Variables = append(resp.Variables, gosnmp.SnmpPDU{Name: cur, Type: gosnmp.EndOfMibView})
				break
			}
			resp.Variables = append(resp.Variables, got)
			cur = got.Name
		}
	case gosnmp.SetRequest:
		if req.Community != f.write {
			resp.Error, resp.ErrorIndex, resp.Variables = gosnmp.NoAccess, 1, req.Variables
			break
		}
		for _, v := range req.Variables {
			f.sets = append(f.sets, v)
			f.vals[v.Name] = v
		}
		resp.Variables = req.Variables
	default:
		return nil
	}
	out, err := resp.MarshalMsg()
	if err != nil {
		return nil
	}
	return out
}

func open(t *testing.T, cfg sdk.Config) *device {
	t.Helper()
	dev, err := Driver{}.Open(context.Background(), cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dev.Close() })
	return dev.(*device)
}

func poll(t *testing.T, d *device) map[string]string {
	t.Helper()
	vars, err := d.poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return vars
}

func expect(t *testing.T, vars map[string]string, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s = %q, want %q", k, vars[k], v)
		}
	}
}

func apcCard() *fakeAgent {
	return newFake(
		str(oidSysDescr, "APC Web/SNMP Management Card (MB:v4.1.0 PF:v6.4.6 PN:apc_hw05_aos_646.bin)"),
		oid(oidSysObjectID, ".1.3.6.1.4.1.318.1.3.27"),
		str(apc+".1.1.1.0", "Smart-UPS 1500"),
		str(apc+".1.2.1.0", "UPS 09.3 (ID18)"),
		str(apc+".1.2.3.0", "AS1234567890"),
		num(apc+".2.1.1.0", 2),
		gauge(apc+".2.2.1.0", 99), gauge(apc+".2.3.1.0", 987),
		ticks(apc+".2.2.3.0", 270000),
		num(apc+".2.2.4.0", 1),
		gauge(apc+".2.2.8.0", 27), gauge(apc+".2.3.4.0", 273),
		gauge(apc+".3.2.1.0", 121), gauge(apc+".3.3.1.0", 1215),
		gauge(apc+".4.1.1.0", 2),
		gauge(apc+".4.2.3.0", 23), gauge(apc+".4.3.3.0", 234),
		num(apc+".3.2.5.0", 4),
		ticks(apc+".5.2.10.0", 2000),
		num(apc+".7.2.3.0", 1),
	)
}

func TestAPC(t *testing.T) {
	f := apcCard()
	addr := f.serve(t)
	d := open(t, sdk.Config{"host": addr, "write_community": "private", "timeout": "1s"})
	if d.m != apcMIB {
		t.Fatalf("MIB %s, want apc", d.m.name)
	}
	expect(t, poll(t, d), map[string]string{
		"ups.status": "OL", "device.mfr": "APC", "device.model": "Smart-UPS 1500", "device.serial": "AS1234567890",
		"battery.charge": "98.7", "battery.runtime": "2700", "battery.voltage": "27.3", "input.voltage": "121.5",
		"ups.load": "23.4", "input.transfer.reason": "blackout", "ups.delay.shutdown": "20", "ups.test.result": "Ok",
	})

	// Commands.
	if err := d.RunCommand(context.Background(), "test.battery.start", ""); err != nil {
		t.Fatal(err)
	}
	if err := d.RunCommand(context.Background(), "shutdown.return", "60"); !errors.Is(err, sdk.ErrNotSupported) {
		t.Errorf("shutdown.return on line: %v", err)
	}
	f.mu.Lock()
	f.vals[apc+".4.1.1.0"] = gauge(apc+".4.1.1.0", 3) // on battery
	f.vals[apc+".2.1.1.0"] = num(apc+".2.1.1.0", 3)   // low
	f.vals[apc+".2.2.4.0"] = num(apc+".2.2.4.0", 2)   // replace
	f.mu.Unlock()
	expect(t, poll(t, d), map[string]string{"ups.status": "OB DISCHRG LB RB"})
	if err := d.RunCommand(context.Background(), "shutdown.return", "60"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	sets := f.sets
	f.mu.Unlock()
	want := []string{apc + ".7.2.2.0=2", apc + ".5.2.10.0=6000", apc + ".6.1.1.0=2"}
	if len(sets) != len(want) {
		t.Fatalf("sets: %v", sets)
	}
	for i, s := range sets {
		got := s.Name + "=" + gosnmp.ToBigInt(s.Value).String()
		if got != want[i] {
			t.Errorf("set %d: %s, want %s", i, got, want[i])
		}
	}
	if sets[1].Type != gosnmp.TimeTicks {
		t.Errorf("shutdown delay set as %v, want TimeTicks", sets[1].Type)
	}

	// Without a write community there are no commands.
	ro := open(t, sdk.Config{"host": addr, "timeout": "1s"})
	if len(ro.Commands()) != 0 {
		t.Errorf("read-only commands: %v", ro.Commands())
	}
	if err := ro.RunCommand(context.Background(), "test.battery.start", ""); !errors.Is(err, sdk.ErrNotSupported) {
		t.Errorf("read-only command: %v", err)
	}
}

// TestIETFv1 uses an SNMPv1 agent that lacks most objects, with an alarm.
func TestIETFv1(t *testing.T) {
	f := newFake(
		str(oidSysDescr, "Generic UPS card"),
		oid(oidSysObjectID, ".1.3.6.1.4.1.99999.1"),
		str(ietf+".1.1.0", "Example"), str(ietf+".1.2.0", "UPS 3000"),
		num(ietf+".2.1.0", 2), num(ietf+".2.3.0", 45), num(ietf+".2.4.0", 100), num(ietf+".2.5.0", 545),
		num(ietf+".4.1.0", 3), num(ietf+".4.4.1.5.1", 30), num(ietf+".9.7.0", 2), num(ietf+".9.8.0", 2),
		gauge(ietf+".6.1.0", 1),
		num(ietf+".6.2.1.1.7", 7), oid(ietf+".6.2.1.2.7", ietf+".6.3.22"),
	)
	addr := f.serve(t)
	d := open(t, sdk.Config{"host": addr, "version": "1", "write_community": "private", "timeout": "1s"})
	if d.m != ietfMIB {
		t.Fatalf("MIB %s, want ietf", d.m.name)
	}
	expect(t, poll(t, d), map[string]string{
		"ups.status": "OL FSD", "ups.alarm": "Shutdown pending", "device.mfr": "Example", "device.model": "UPS 3000",
		"battery.runtime": "2700", "battery.charge": "100", "battery.voltage": "54.5", "ups.load": "30",
		"battery.runtime.low": "120", "ups.beeper.status": "enabled",
	})
	if len(d.missing) == 0 {
		t.Error("v1 fallback did not record missing objects")
	}
	poll(t, d) // the second poll skips the missing objects

	for _, c := range []struct{ cmd, param string }{{"test.battery.start.deep", ""}, {"shutdown.return", "45"}, {"shutdown.stop", ""}} {
		if err := d.RunCommand(context.Background(), c.cmd, c.param); err != nil {
			t.Fatalf("%s: %v", c.cmd, err)
		}
	}
	var got []string
	f.mu.Lock()
	for _, s := range f.sets {
		v := gosnmp.ToBigInt(s.Value).String()
		if s.Type == gosnmp.ObjectIdentifier {
			v = s.Value.(string)
		}
		got = append(got, s.Name+"="+v)
	}
	f.mu.Unlock()
	want := []string{ietf + ".7.1.0=" + ietf + ".7.7.4", ietf + ".8.5.0=1", ietf + ".8.2.0=45", ietf + ".8.2.0=-1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("sets:\n got %v\nwant %v", got, want)
	}
	if err := d.RunCommand(context.Background(), "shutdown.return", "soon"); !errors.Is(err, sdk.ErrInvalid) {
		t.Errorf("bad delay: %v", err)
	}
}

// TestRoundTrip reads a simulated UPS through Currently's own SNMP agent,
// with v2c and v3.
func TestRoundTrip(t *testing.T) {
	c := core.New(quiet)
	if err := c.AddUPS(core.UPSSpec{Name: "sim", Driver: simulator.New(),
		Config: sdk.Config{"scenario": "onbattery", "interval": "100ms"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	for deadline := time.Now().Add(3 * time.Second); ; {
		if u, _ := c.GetUPS("sim"); u.State == sdk.StateConnected && u.Status().Has(sdk.FlagOnBattery) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("simulator did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	agent, err := snmp.New(c, snmp.Options{Community: "public", Log: quiet, Version: "test",
		Users: []snmp.User{{Name: "mon", Auth: "sha256", AuthPassword: "authpassword", Privacy: "aes", PrivacyPassword: "privpassword"}}})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = agent.Serve(ctx, pc) }()
	addr := pc.LocalAddr().String()

	for name, cfg := range map[string]sdk.Config{
		"v2c": {"host": addr},
		"v3": {"host": addr, "version": "3", "username": "mon", "auth": "SHA-256", "auth_password": "authpassword",
			"privacy": "aes", "privacy_password": "privpassword"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg["timeout"] = "1s"
			d := open(t, cfg)
			if d.m != ietfMIB {
				t.Fatalf("MIB %s", d.m.name)
			}
			vars := poll(t, d)
			u, _ := c.GetUPS("sim")
			st := sdk.ParseStatus(vars["ups.status"])
			if !st.Has(sdk.FlagOnBattery) {
				t.Errorf("status %q, want OB", vars["ups.status"])
			}
			if vars["device.model"] != u.Value("device.model") || vars["battery.charge"] == "" || vars["input.voltage"] == "" {
				t.Errorf("vars %v\nsim %v", vars, u.Vars)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	for cfg, want := range map[string]sdk.Config{
		"host is required":             {},
		"username is required":         {"host": "x", "version": "3"},
		"privacy needs an auth":        {"host": "x", "version": "3", "username": "u", "privacy": "aes", "privacy_password": "p"},
		`version "4"`:                  {"host": "x", "version": "4"},
		`mib "powerware"`:              {"host": "x", "mib": "powerware"},
		`unknown option "comunity"`:    {"host": "x", "comunity": "public"},
		"auth_password or auth_passwo": {"host": "x", "version": "3", "username": "u", "auth": "sha"},
	} {
		err := Driver{}.ValidateConfig(want)
		if err == nil || !strings.Contains(err.Error(), cfg) {
			t.Errorf("%v: error %v, want %q", want, err, cfg)
		}
	}
	if err := (Driver{}).ValidateConfig(sdk.Config{"host": "ups.example:1161", "version": "v2c"}); err != nil {
		t.Error(err)
	}
}
