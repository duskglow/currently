package snmp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/duskglow/currently/pkg/sdk"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeHost serves fixed UPS snapshots.
type fakeHost struct {
	mu   sync.Mutex
	list []sdk.UPS
}

func vars(kv ...string) map[string]sdk.Variable {
	m := map[string]sdk.Variable{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = sdk.Variable{Value: kv[i+1]}
	}
	return m
}

func (h *fakeHost) set(name string, f func(*sdk.UPS)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.list {
		if h.list[i].Name == name {
			f(&h.list[i])
		}
	}
}

func (h *fakeHost) ListUPS() []sdk.UPS {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]sdk.UPS(nil), h.list...)
}

func (h *fakeHost) GetUPS(name string) (sdk.UPS, bool) {
	for _, u := range h.ListUPS() {
		if u.Name == name {
			return u, true
		}
	}
	return sdk.UPS{}, false
}

func (h *fakeHost) History(string, string) ([]sdk.Sample, error) { return nil, nil }
func (h *fakeHost) RecentEvents(int) []sdk.Event                 { return nil }
func (h *fakeHost) Subscribe(ctx context.Context, _ int) <-chan sdk.Event {
	ch := make(chan sdk.Event)
	go func() { <-ctx.Done(); close(ch) }()
	return ch
}
func (h *fakeHost) RunCommand(context.Context, string, string, string) error {
	return errors.New("read only")
}
func (h *fakeHost) SetVar(context.Context, string, string, string) error {
	return errors.New("read only")
}
func (h *fakeHost) SetForcedShutdown(context.Context, string) error { return errors.New("read only") }

func newHost() *fakeHost {
	return &fakeHost{list: []sdk.UPS{
		{Name: "rack", Description: "Rack UPS", Driver: "usbhid", State: sdk.StateConnected, Vars: vars(
			"device.mfr", "CyberPower", "device.model", "CP1500PFCLCD", "ups.firmware", "CR01505B4",
			"ups.status", "OL CHRG", "battery.charge", "87", "battery.runtime", "2730",
			"battery.voltage", "24.1", "input.voltage", "121.0", "input.frequency", "60.0",
			"output.voltage", "120.5", "ups.load", "23", "ups.realpower.nominal", "900",
			"input.transfer.low", "88", "input.transfer.high", "139", "battery.runtime.low", "300",
			"ups.beeper.status", "enabled")},
		{Name: "lab", State: sdk.StateConnected, Vars: vars("ups.status", "OB DISCHRG LB", "battery.charge", "9")},
	}}
}

const (
	community = "s3cret"
	authPass  = "authpassword"
	privPass  = "privpassword"
)

var testUsers = []User{
	{Name: "noauth", Auth: "none"},
	{Name: "md5", Auth: "md5", AuthPassword: authPass},
	{Name: "sha", Auth: "sha", AuthPassword: authPass},
	{Name: "sha224", Auth: "sha224", AuthPassword: authPass},
	{Name: "sha256", Auth: "sha256", AuthPassword: authPass},
	{Name: "sha384", Auth: "sha384", AuthPassword: authPass},
	{Name: "sha512", Auth: "sha512", AuthPassword: authPass},
	{Name: "priv", Auth: "sha", AuthPassword: authPass, Privacy: "aes", PrivacyPassword: privPass},
	{Name: "priv256", Auth: "sha256", AuthPassword: authPass, Privacy: "aes", PrivacyPassword: privPass},
}

// startAgent serves an agent on a loopback port.
func startAgent(t *testing.T, host *fakeHost, opts Options) (*Agent, string) {
	t.Helper()
	opts.Log, opts.Version = quiet, "test"
	a, err := New(host, opts)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Serve(ctx, pc); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return a, pc.LocalAddr().String()
}

func client(t *testing.T, addr string, f func(*gosnmp.GoSNMP)) *gosnmp.GoSNMP {
	t.Helper()
	host, port, _ := net.SplitHostPort(addr)
	var p uint16
	for _, c := range port {
		p = p*10 + uint16(c-'0')
	}
	g := &gosnmp.GoSNMP{Target: host, Port: p, Version: gosnmp.Version2c, Community: community,
		Timeout: time.Second, Retries: 0, MaxOids: 60}
	if f != nil {
		f(g)
	}
	if err := g.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Conn.Close() })
	return g
}

func getInt(t *testing.T, g *gosnmp.GoSNMP, oid string) int64 {
	t.Helper()
	r, err := g.Get([]string{oid})
	if err != nil {
		t.Fatalf("get %s: %v", oid, err)
	}
	v := r.Variables[0]
	if v.Type == gosnmp.NoSuchObject || v.Type == gosnmp.NoSuchInstance {
		t.Fatalf("get %s: %v", oid, v.Type)
	}
	return gosnmp.ToBigInt(v.Value).Int64()
}

func getString(t *testing.T, g *gosnmp.GoSNMP, oid string) string {
	t.Helper()
	r, err := g.Get([]string{oid})
	if err != nil {
		t.Fatalf("get %s: %v", oid, err)
	}
	b, _ := r.Variables[0].Value.([]byte)
	return string(b)
}

func TestUPSMIBv2c(t *testing.T) {
	_, addr := startAgent(t, newHost(), Options{Community: community})
	g := client(t, addr, nil)

	if s := getString(t, g, ".1.3.6.1.2.1.1.1.0"); !strings.HasPrefix(s, "Currently test") {
		t.Errorf("sysDescr = %q", s)
	}
	for oid, want := range map[string]int64{
		".1.3.6.1.2.1.33.1.2.1.0":     2,   // upsBatteryStatus: normal
		".1.3.6.1.2.1.33.1.2.2.0":     0,   // upsSecondsOnBattery
		".1.3.6.1.2.1.33.1.2.3.0":     46,  // upsEstimatedMinutesRemaining (2730 s)
		".1.3.6.1.2.1.33.1.2.4.0":     87,  // upsEstimatedChargeRemaining
		".1.3.6.1.2.1.33.1.2.5.0":     241, // upsBatteryVoltage, 0.1 V
		".1.3.6.1.2.1.33.1.3.2.0":     1,   // upsInputNumLines
		".1.3.6.1.2.1.33.1.3.3.1.2.1": 600, // upsInputFrequency, 0.1 Hz
		".1.3.6.1.2.1.33.1.3.3.1.3.1": 121, // upsInputVoltage
		".1.3.6.1.2.1.33.1.4.1.0":     3,   // upsOutputSource: normal
		".1.3.6.1.2.1.33.1.4.4.1.2.1": 121, // upsOutputVoltage (120.5 rounds up)
		".1.3.6.1.2.1.33.1.4.4.1.5.1": 23,  // upsOutputPercentLoad
		".1.3.6.1.2.1.33.1.6.1.0":     0,   // upsAlarmsPresent
		".1.3.6.1.2.1.33.1.9.6.0":     900, // upsConfigOutputPower
		".1.3.6.1.2.1.33.1.9.7.0":     5,   // upsConfigLowBattTime
		".1.3.6.1.2.1.33.1.9.8.0":     2,   // upsConfigAudibleStatus: enabled
		".1.3.6.1.2.1.33.1.9.9.0":     88,  // upsConfigLowVoltageTransferPoint
		".1.3.6.1.2.1.33.1.9.10.0":    139, // upsConfigHighVoltageTransferPoint
	} {
		if got := getInt(t, g, oid); got != want {
			t.Errorf("%s = %d, want %d", oid, got, want)
		}
	}
	if s := getString(t, g, ".1.3.6.1.2.1.33.1.1.2.0"); s != "CP1500PFCLCD" {
		t.Errorf("upsIdentModel = %q", s)
	}

	// Missing objects and instances.
	r, err := g.Get([]string{".1.3.6.1.2.1.33.1.2.6.0", ".1.3.6.1.2.1.33.1.2.4.1", ".1.3.6.1.4.1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Variables[0].Type != gosnmp.NoSuchObject || r.Variables[1].Type != gosnmp.NoSuchInstance ||
		r.Variables[2].Type != gosnmp.NoSuchObject {
		t.Errorf("missing objects: %v %v %v", r.Variables[0].Type, r.Variables[1].Type, r.Variables[2].Type)
	}

	// Walks: GetNext and GetBulk agree, and stay sorted.
	walk, err := g.WalkAll(".1.3.6.1.2.1.33")
	if err != nil {
		t.Fatal(err)
	}
	bulk, err := g.BulkWalkAll(".1.3.6.1.2.1.33")
	if err != nil {
		t.Fatal(err)
	}
	if len(walk) < 25 || len(walk) != len(bulk) {
		t.Fatalf("walk %d objects, bulk walk %d", len(walk), len(bulk))
	}
	for i := range walk {
		if walk[i].Name != bulk[i].Name {
			t.Fatalf("walk and bulk walk differ at %d: %s %s", i, walk[i].Name, bulk[i].Name)
		}
	}
	full, err := g.BulkWalkAll(".1.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(full) <= len(walk) {
		t.Errorf("full walk has %d objects", len(full))
	}

	// The community selects another UPS.
	lab := client(t, addr, func(g *gosnmp.GoSNMP) { g.Community = community + "@lab" })
	if got := getInt(t, lab, ".1.3.6.1.2.1.33.1.2.1.0"); got != 3 {
		t.Errorf("lab upsBatteryStatus = %d, want 3 (low)", got)
	}
	if got := getInt(t, lab, ".1.3.6.1.2.1.33.1.4.1.0"); got != 5 {
		t.Errorf("lab upsOutputSource = %d, want 5 (battery)", got)
	}
	if got := getInt(t, lab, ".1.3.6.1.2.1.33.1.6.1.0"); got != 2 {
		t.Errorf("lab upsAlarmsPresent = %d, want 2", got)
	}

	// Writes are refused.
	if r, err := g.Set([]gosnmp.SnmpPDU{{Name: ".1.3.6.1.2.1.1.4.0", Type: gosnmp.OctetString, Value: "x"}}); err == nil &&
		r.Error != gosnmp.NotWritable {
		t.Errorf("set: %v", r.Error)
	}

	// Wrong communities get no answer.
	for _, c := range []string{"public", community + "@nope", "x@rack"} {
		bad := client(t, addr, func(g *gosnmp.GoSNMP) { g.Community = c; g.Timeout = 200 * time.Millisecond })
		if _, err := bad.Get([]string{".1.3.6.1.2.1.1.1.0"}); err == nil {
			t.Errorf("community %q accepted", c)
		}
	}
}

func TestSNMPv1(t *testing.T) {
	_, addr := startAgent(t, newHost(), Options{Community: community})
	g := client(t, addr, func(g *gosnmp.GoSNMP) { g.Version = gosnmp.Version1 })
	if got := getInt(t, g, ".1.3.6.1.2.1.33.1.2.4.0"); got != 87 {
		t.Errorf("charge = %d", got)
	}
	walk, err := g.WalkAll(".1.3.6.1.2.1.33.1.2")
	if err != nil || len(walk) == 0 {
		t.Fatalf("v1 walk: %d objects, %v", len(walk), err)
	}
	r, err := g.Get([]string{".1.3.6.1.2.1.33.1.2.6.0"})
	if err != nil || r.Error != gosnmp.NoSuchName || r.ErrorIndex != 1 {
		t.Errorf("v1 get of a missing object: %+v %v", r, err)
	}
}

func TestSNMPv3(t *testing.T) {
	a, addr := startAgent(t, newHost(), Options{Users: testUsers})
	for _, tc := range []struct {
		user  string
		flags gosnmp.SnmpV3MsgFlags
		auth  gosnmp.SnmpV3AuthProtocol
		priv  gosnmp.SnmpV3PrivProtocol
	}{
		{"noauth", gosnmp.NoAuthNoPriv, gosnmp.NoAuth, gosnmp.NoPriv},
		{"md5", gosnmp.AuthNoPriv, gosnmp.MD5, gosnmp.NoPriv},
		{"sha", gosnmp.AuthNoPriv, gosnmp.SHA, gosnmp.NoPriv},
		{"sha224", gosnmp.AuthNoPriv, gosnmp.SHA224, gosnmp.NoPriv},
		{"sha256", gosnmp.AuthNoPriv, gosnmp.SHA256, gosnmp.NoPriv},
		{"sha384", gosnmp.AuthNoPriv, gosnmp.SHA384, gosnmp.NoPriv},
		{"sha512", gosnmp.AuthNoPriv, gosnmp.SHA512, gosnmp.NoPriv},
		{"priv", gosnmp.AuthPriv, gosnmp.SHA, gosnmp.AES},
		{"priv256", gosnmp.AuthPriv, gosnmp.SHA256, gosnmp.AES},
	} {
		t.Run(tc.user, func(t *testing.T) {
			g := client(t, addr, func(g *gosnmp.GoSNMP) {
				g.Version, g.SecurityModel, g.MsgFlags = gosnmp.Version3, gosnmp.UserSecurityModel, tc.flags
				g.SecurityParameters = &gosnmp.UsmSecurityParameters{UserName: tc.user,
					AuthenticationProtocol: tc.auth, AuthenticationPassphrase: authPass,
					PrivacyProtocol: tc.priv, PrivacyPassphrase: privPass}
			})
			if got := getInt(t, g, ".1.3.6.1.2.1.33.1.2.4.0"); got != 87 {
				t.Errorf("charge = %d", got)
			}
			walk, err := g.BulkWalkAll(".1.3.6.1.2.1.33")
			if err != nil || len(walk) < 25 {
				t.Errorf("bulk walk: %d objects, %v", len(walk), err)
			}
			if string(g.SecurityParameters.(*gosnmp.UsmSecurityParameters).AuthoritativeEngineID) != string(a.EngineID()) {
				t.Error("engine ID not discovered")
			}
		})
	}

	t.Run("context", func(t *testing.T) {
		g := client(t, addr, func(g *gosnmp.GoSNMP) {
			g.Version, g.SecurityModel, g.MsgFlags = gosnmp.Version3, gosnmp.UserSecurityModel, gosnmp.AuthPriv
			g.ContextName = "lab"
			g.SecurityParameters = &gosnmp.UsmSecurityParameters{UserName: "priv",
				AuthenticationProtocol: gosnmp.SHA, AuthenticationPassphrase: authPass,
				PrivacyProtocol: gosnmp.AES, PrivacyPassphrase: privPass}
		})
		if got := getInt(t, g, ".1.3.6.1.2.1.33.1.2.4.0"); got != 9 {
			t.Errorf("lab charge = %d", got)
		}
	})

	for name, f := range map[string]func(*gosnmp.UsmSecurityParameters, *gosnmp.GoSNMP){
		"wrong password": func(p *gosnmp.UsmSecurityParameters, _ *gosnmp.GoSNMP) { p.AuthenticationPassphrase = "wrongpassword" },
		"unknown user":   func(p *gosnmp.UsmSecurityParameters, _ *gosnmp.GoSNMP) { p.UserName = "nobody" },
		"wrong privacy":  func(p *gosnmp.UsmSecurityParameters, _ *gosnmp.GoSNMP) { p.PrivacyPassphrase = "wrongprivacy" },
		"no privacy": func(p *gosnmp.UsmSecurityParameters, g *gosnmp.GoSNMP) {
			p.PrivacyProtocol = gosnmp.NoPriv
			g.MsgFlags = gosnmp.AuthNoPriv
		},
		"unknown context": func(_ *gosnmp.UsmSecurityParameters, g *gosnmp.GoSNMP) { g.ContextName = "nope" },
	} {
		t.Run(name, func(t *testing.T) {
			g := client(t, addr, func(g *gosnmp.GoSNMP) {
				g.Version, g.SecurityModel, g.MsgFlags = gosnmp.Version3, gosnmp.UserSecurityModel, gosnmp.AuthPriv
				g.Timeout = 300 * time.Millisecond
				p := &gosnmp.UsmSecurityParameters{UserName: "priv",
					AuthenticationProtocol: gosnmp.SHA, AuthenticationPassphrase: authPass,
					PrivacyProtocol: gosnmp.AES, PrivacyPassphrase: privPass}
				f(p, g)
				g.SecurityParameters = p
			})
			r, err := g.Get([]string{".1.3.6.1.2.1.33.1.2.4.0"})
			if err == nil && r.Error == gosnmp.NoError && len(r.Variables) == 1 && r.Variables[0].Type == gosnmp.Integer {
				t.Errorf("request accepted: %+v", r.Variables)
			}
		})
	}
}

func TestV3TimeWindow(t *testing.T) {
	now := time.Now()
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	a, addr := startAgent(t, newHost(), Options{Users: testUsers, Now: clock})
	g := client(t, addr, func(g *gosnmp.GoSNMP) {
		g.Version, g.SecurityModel, g.MsgFlags = gosnmp.Version3, gosnmp.UserSecurityModel, gosnmp.AuthNoPriv
		g.SecurityParameters = &gosnmp.UsmSecurityParameters{UserName: "sha",
			AuthenticationProtocol: gosnmp.SHA, AuthenticationPassphrase: authPass}
	})
	getInt(t, g, ".1.3.6.1.2.1.33.1.2.4.0")
	mu.Lock()
	now = now.Add(10 * time.Minute) // the manager's cached engine time is now stale
	mu.Unlock()
	// The agent rejects the stale time with an authenticated report, from
	// which the manager learns the engine time and retries.
	if got := getInt(t, g, ".1.3.6.1.2.1.33.1.2.4.0"); got != 87 {
		t.Errorf("charge = %d after resync", got)
	}
	a.mu.Lock()
	n := a.stats[statTimeWindow]
	a.mu.Unlock()
	if n == 0 {
		t.Error("notInTimeWindows not counted")
	}
}

func TestTraps(t *testing.T) {
	recv, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer recv.Close()
	host := newHost()
	a, _ := startAgent(t, host, Options{Community: community,
		Traps: []TrapTarget{{Addr: recv.LocalAddr().String(), Community: "traps"}}})

	read := func() []VarBind {
		t.Helper()
		buf := make([]byte, 4096)
		_ = recv.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := recv.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		top, err := newDecoder(buf[:n]).sub(tagSequence)
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := top.int(); v != 1 {
			t.Fatalf("trap version %d", v)
		}
		if c, _, _ := top.octets(); string(c) != "traps" {
			t.Fatalf("trap community %q", c)
		}
		p, err := top.pdu()
		if err != nil || p.Type != pduTrapV2 {
			t.Fatalf("trap PDU %x %v", p.Type, err)
		}
		return p.VarBinds
	}
	trapOID := func(vbs []VarBind) string {
		if len(vbs) < 2 || vbs[1].Name.Compare(oidSnmpTrapOID) != 0 {
			t.Fatalf("no snmpTrapOID: %v", vbs)
		}
		return vbs[1].Value.OID.String()
	}

	for deadline := time.Now().Add(2 * time.Second); ; { // wait for Serve
		a.mu.Lock()
		ready := a.conn != nil
		a.mu.Unlock()
		if ready || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.refreshAll() // the lab UPS is on battery with a low battery
	got := map[string]int{}
	for range 3 { // onBattery, alarm added (onBattery), alarm added (lowBattery)
		got[trapOID(read())]++
	}
	if got["1.3.6.1.2.1.33.2.1"] != 1 || got["1.3.6.1.2.1.33.2.3"] != 2 {
		t.Errorf("traps: %v", got)
	}

	host.set("lab", func(u *sdk.UPS) { u.Vars = vars("ups.status", "OL CHRG", "battery.charge", "20") })
	a.refreshAll()
	removed := 0
	for range 2 {
		vbs := read()
		if trapOID(vbs) == "1.3.6.1.2.1.33.2.4" {
			removed++
		}
		if last := vbs[len(vbs)-1]; string(last.Value.Bytes) != "lab" {
			t.Errorf("trap names UPS %q", last.Value.Bytes)
		}
	}
	if removed != 2 {
		t.Errorf("%d alarm removed traps, want 2", removed)
	}
}

func TestBER(t *testing.T) {
	for _, v := range []int64{0, 1, -1, 127, 128, -128, -129, 255, 256, 1 << 31, -(1 << 40)} {
		got, err := decodeInt(encodeInt(v))
		if err != nil || got != v {
			t.Errorf("int %d → %d, %v", v, got, err)
		}
	}
	for _, v := range []uint64{0, 127, 128, 255, 1<<32 - 1, 1<<64 - 1} {
		got, err := decodeUint(encodeUint(v))
		if err != nil || got != v {
			t.Errorf("uint %d → %d, %v", v, got, err)
		}
	}
	for _, s := range []string{"1.3.6.1.2.1.33.1.2.4.0", "1.3.6.1.4.1.4294967295", "2.999.3", "0.0"} {
		o := mustOID(s)
		got, err := decodeOID(encodeOID(o))
		if err != nil || got.String() != s {
			t.Errorf("OID %s → %s, %v", s, got, err)
		}
	}
	if mustOID("1.3.6.1.2").Compare(mustOID("1.3.6.1.10")) >= 0 || mustOID("1.3").Compare(mustOID("1.3.0")) >= 0 {
		t.Error("OID order")
	}
}

// FuzzHandle checks that no message crashes the agent.
func FuzzHandle(f *testing.F) {
	a, err := New(newHost(), Options{Community: community, Users: testUsers[:3], Log: quiet})
	if err != nil {
		f.Fatal(err)
	}
	get := PDU{Type: pduGetBulk, RequestID: 1, ErrIndex: 10, VarBinds: []VarBind{{mustOID("1.3.6.1"), nullValue()}}}
	var c []byte
	c = appendInt(c, 1)
	c = appendOctets(c, []byte(community))
	c = append(c, get.encode()...)
	f.Add(appendTLV(nil, tagSequence, c))
	f.Add(a.encodeV3(1, flagReportable, nil, nil, nil, get))
	f.Fuzz(func(t *testing.T, pkt []byte) { a.Handle(pkt) })
}

func TestProbe(t *testing.T) {
	a, addr := startAgent(t, newHost(), Options{Community: community})
	id, err := Probe(addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if string(id) != string(a.EngineID()) {
		t.Errorf("engine ID %x, want %x", id, a.EngineID())
	}
}
