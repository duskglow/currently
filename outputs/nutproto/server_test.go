package nutproto

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nut-ng/nut-ng/drivers/simulator"
	"github.com/nut-ng/nut-ng/internal/core"
	"github.com/nut-ng/nut-ng/internal/nutwire"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestSplit(t *testing.T) {
	tests := map[string][]string{
		`GET VAR ups battery.charge`:          {"GET", "VAR", "ups", "battery.charge"},
		`SET VAR ups ups.id "my \"big\" ups"`: {"SET", "VAR", "ups", "ups.id", `my "big" ups`},
		`  LIST   UPS  `:                      {"LIST", "UPS"},
		`PASSWORD "back\\slash"`:              {"PASSWORD", `back\slash`},
		`SET VAR u v ""`:                      {"SET", "VAR", "u", "v", ""},
	}
	for in, want := range tests {
		got, err := nutwire.Split(in)
		if err != nil || strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("nutwire.Split(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := nutwire.Split(`GET "open`); err == nil {
		t.Error("unterminated quote accepted")
	}
	if got := nutwire.Quote(`a "b" \c`); got != `"a \"b\" \\c"` {
		t.Errorf("quote = %s", got)
	}
}

// startServer runs a core with a simulated UPS "sim" and a NUT server.
func startServer(t *testing.T, tlsConf *tls.Config) (addr string, c *core.Core) {
	t.Helper()
	c = core.New(quiet)
	if err := c.AddUPS(core.UPSSpec{Name: "sim", Description: "Test UPS", Driver: simulator.New(),
		Config: sdk.Config{"interval": "1h"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddUPS(core.UPSSpec{Name: "dead", Driver: deadDriver{}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	go func() { _ = c.Run(ctx); done <- struct{}{} }()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := New(c, Options{Version: "test", Log: quiet, TLS: tlsConf, Users: map[string]User{
		"mon":   {Password: "secret", Role: RoleSecondary},
		"prim":  {Password: "secret", Role: RolePrimary},
		"admin": {Password: "secret", Role: RoleAdmin},
	}})
	go func() { _ = s.Serve(ctx, ln); done <- struct{}{} }()
	t.Cleanup(func() { cancel(); <-done; <-done })

	deadline := time.Now().Add(3 * time.Second)
	for {
		if u, _ := c.GetUPS("sim"); u.State == sdk.StateConnected {
			return ln.Addr().String(), c
		}
		if time.Now().After(deadline) {
			t.Fatal("simulator did not connect")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type deadDriver struct{}

func (deadDriver) Info() sdk.Info                                     { return sdk.Info{Name: "dead"} }
func (deadDriver) Discover(context.Context) ([]sdk.Discovered, error) { return nil, nil }
func (deadDriver) ValidateConfig(sdk.Config) error                    { return nil }
func (deadDriver) Open(context.Context, sdk.Config, *slog.Logger) (sdk.Device, error) {
	return nil, io.ErrUnexpectedEOF
}

type client struct {
	t  *testing.T
	nc net.Conn
	r  *bufio.Reader
}

func dial(t *testing.T, addr string) *client {
	t.Helper()
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	return &client{t: t, nc: nc, r: bufio.NewReader(nc)}
}

func (c *client) line() string {
	c.t.Helper()
	_ = c.nc.SetReadDeadline(time.Now().Add(3 * time.Second))
	s, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return strings.TrimRight(s, "\n")
}

// do sends a command and returns the reply, or all lines of a LIST reply.
func (c *client) do(cmd string) string {
	c.t.Helper()
	if _, err := io.WriteString(c.nc, cmd+"\n"); err != nil {
		c.t.Fatal(err)
	}
	first := c.line()
	if !strings.HasPrefix(first, "BEGIN LIST") {
		return first
	}
	lines := []string{first}
	for {
		l := c.line()
		lines = append(lines, l)
		if strings.HasPrefix(l, "END LIST") {
			return strings.Join(lines, "\n")
		}
	}
}

func expect(t *testing.T, c *client, cmd, want string) {
	t.Helper()
	if got := c.do(cmd); got != want {
		t.Errorf("%s:\n got %q\nwant %q", cmd, got, want)
	}
}

func expectPrefix(t *testing.T, c *client, cmd, want string) string {
	t.Helper()
	got := c.do(cmd)
	if !strings.HasPrefix(got, want) {
		t.Errorf("%s:\n got %q\nwant prefix %q", cmd, got, want)
	}
	return got
}

func TestReadCommands(t *testing.T) {
	addr, _ := startServer(t, nil)
	c := dial(t, addr)

	expect(t, c, "NETVER", "1.3")
	expectPrefix(t, c, "VER", "Network UPS Tools upsd")
	expect(t, c, "LIST UPS", "BEGIN LIST UPS\nUPS sim \"Test UPS\"\nUPS dead \"Description unavailable\"\nEND LIST UPS")
	expect(t, c, "GET VAR sim battery.charge", `VAR sim battery.charge "100"`)
	expect(t, c, "get var sim ups.status", `VAR sim ups.status "OL"`)
	expect(t, c, "GET VAR sim nope", "ERR VAR-NOT-SUPPORTED")
	expect(t, c, "GET VAR nope battery.charge", "ERR UNKNOWN-UPS")
	expect(t, c, "GET VAR dead ups.status", "ERR DRIVER-NOT-CONNECTED")
	expect(t, c, "GET UPSDESC sim", `UPSDESC sim "Test UPS"`)
	expect(t, c, "GET TYPE sim battery.charge.low", "TYPE sim battery.charge.low RW NUMBER")
	expect(t, c, "GET TYPE sim ups.model", "TYPE sim ups.model STRING:64")
	expect(t, c, "GET DESC sim battery.charge", `DESC sim battery.charge "Battery charge"`)
	expect(t, c, "GET CMDDESC sim beeper.disable", `CMDDESC sim beeper.disable "Disable the UPS beeper"`)
	expect(t, c, "GET NUMLOGINS sim", "NUMLOGINS sim 0")

	vars := expectPrefix(t, c, "LIST VAR sim", "BEGIN LIST VAR sim\n")
	if !strings.Contains(vars, "\nVAR sim battery.runtime \"") || !strings.HasSuffix(vars, "END LIST VAR sim") {
		t.Errorf("LIST VAR:\n%s", vars)
	}
	expect(t, c, "LIST RW sim", "BEGIN LIST RW sim\nRW sim battery.charge.low \"20\"\nRW sim battery.runtime.low \"120\"\nRW sim ups.load \"35\"\nEND LIST RW sim")
	cmds := expectPrefix(t, c, "LIST CMD sim", "BEGIN LIST CMD sim\n")
	if !strings.Contains(cmds, "CMD sim sim.power.fail") {
		t.Errorf("LIST CMD:\n%s", cmds)
	}
	expect(t, c, "LIST ENUM sim ups.status", "BEGIN LIST ENUM sim ups.status\nEND LIST ENUM sim ups.status")
	expect(t, c, "LIST VAR dead", "ERR DRIVER-NOT-CONNECTED")
	expect(t, c, "FROBNICATE", "ERR UNKNOWN-COMMAND")
	expect(t, c, `GET "open`, "ERR INVALID-ARGUMENT")
	expect(t, c, "STARTTLS", "ERR FEATURE-NOT-CONFIGURED")
	expect(t, c, "LOGOUT", "OK Goodbye")
}

func TestAuthAndUpsmonFlow(t *testing.T) {
	addr, host := startServer(t, nil)

	anon := dial(t, addr)
	expect(t, anon, "LOGIN sim", "ERR USERNAME-REQUIRED")
	expect(t, anon, "INSTCMD sim beeper.disable", "ERR USERNAME-REQUIRED")

	bad := dial(t, addr)
	expect(t, bad, "USERNAME prim", "OK")
	expect(t, bad, "USERNAME again", "ERR ALREADY-SET-USERNAME")
	expect(t, bad, "PASSWORD wrong", "OK")
	expect(t, bad, "LOGIN sim", "ERR ACCESS-DENIED")

	// A secondary upsmon may log in but not set FSD.
	mon := dial(t, addr)
	expect(t, mon, "USERNAME mon", "OK")
	expect(t, mon, "PASSWORD secret", "OK")
	expect(t, mon, "LOGIN sim", "OK")
	expect(t, mon, "LOGIN sim", "ERR ALREADY-LOGGED-IN")
	expect(t, mon, "FSD sim", "ERR ACCESS-DENIED")
	expect(t, mon, "PRIMARY sim", "ERR ACCESS-DENIED")

	// A primary upsmon: login, PRIMARY (or MASTER), then FSD.
	prim := dial(t, addr)
	expect(t, prim, "USERNAME prim", "OK")
	expect(t, prim, "PASSWORD secret", "OK")
	expect(t, prim, "LOGIN sim", "OK")
	expect(t, prim, "PRIMARY sim", "OK PRIMARY-GRANTED")
	expect(t, prim, "MASTER sim", "OK MASTER-GRANTED")
	expect(t, prim, "GET NUMLOGINS sim", "NUMLOGINS sim 2")
	expect(t, prim, "LIST CLIENT sim", "BEGIN LIST CLIENT sim\nCLIENT sim 127.0.0.1\nCLIENT sim 127.0.0.1\nEND LIST CLIENT sim")
	expect(t, prim, "INSTCMD sim beeper.disable", "ERR ACCESS-DENIED")
	expect(t, prim, "FSD sim", "OK FSD-SET")
	expect(t, mon, "GET VAR sim ups.status", `VAR sim ups.status "OL FSD"`)

	// Logging out drops the login count.
	expect(t, mon, "LOGOUT", "OK Goodbye")
	deadline := time.Now().Add(2 * time.Second)
	for prim.do("GET NUMLOGINS sim") != "NUMLOGINS sim 1" {
		if time.Now().After(deadline) {
			t.Fatal("login count did not drop after LOGOUT")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Admin: commands and variables.
	adm := dial(t, addr)
	expect(t, adm, "USERNAME admin", "OK")
	expect(t, adm, "PASSWORD secret", "OK")
	expect(t, adm, "INSTCMD sim beeper.disable", "OK")
	expect(t, adm, "INSTCMD sim load.off", "ERR CMD-NOT-SUPPORTED")
	expect(t, adm, `SET VAR sim ups.load "80"`, "OK")
	expect(t, adm, `SET VAR sim ups.load "lots"`, "ERR INVALID-VALUE")
	expect(t, adm, `SET VAR sim ups.status "OB"`, "ERR READONLY")
	expect(t, adm, `SET VAR sim nope "1"`, "ERR VAR-NOT-SUPPORTED")
	deadline = time.Now().Add(2 * time.Second)
	for {
		u, _ := host.GetUPS("sim")
		if u.Value("ups.beeper.status") == "disabled" && u.Value("ups.load") == "80" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("changes not applied: beeper %s, load %s", u.Value("ups.beeper.status"), u.Value("ups.load"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLongLine(t *testing.T) {
	addr, _ := startServer(t, nil)
	c := dial(t, addr)
	expect(t, c, strings.Repeat("A", 2000), "ERR TOO-LONG")
}

func TestStartTLS(t *testing.T) {
	cert := selfSigned(t)
	addr, _ := startServer(t, &tls.Config{Certificates: []tls.Certificate{cert}})
	c := dial(t, addr)
	expect(t, c, "STARTTLS", "OK STARTTLS")
	tc := tls.Client(c.nc, &tls.Config{InsecureSkipVerify: true})
	if err := tc.Handshake(); err != nil {
		t.Fatal(err)
	}
	c.nc, c.r = tc, bufio.NewReader(tc)
	expect(t, c, "GET VAR sim ups.status", `VAR sim ups.status "OL"`)
	expect(t, c, "STARTTLS", "ERR ALREADY-SSL-MODE")
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "nutng-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
