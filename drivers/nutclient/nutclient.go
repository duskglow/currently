// Package nutclient is the "nut" driver: it reads a UPS from a NUT server
// (another nutng, or NUT's upsd) over the network. A computer powered by a
// UPS attached to another machine uses it to follow that machine, as a
// NUT upsmon secondary does: with a username, it logs in, so the primary
// waits for it before cutting power.
package nutclient

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nut-ng/nut-ng/internal/nutwire"
	"github.com/nut-ng/nut-ng/internal/suggest"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

const (
	// DriverName is the name used in `driver:` in nutng.yaml.
	DriverName = "nut"
	// Version of the driver.
	Version = "0.1.0"
	// DefaultPort is the NUT protocol port.
	DefaultPort = "3493"
)

// Options select the server and UPS.
type Options struct {
	// Host is "host" or "host:port".
	Host string `yaml:"host"`
	// UPS is the UPS's name on the server; it may be left out when the
	// server has only one.
	UPS         string `yaml:"ups"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	PasswordEnv string `yaml:"password_env"`
	// TLS upgrades the connection with STARTTLS; TLSInsecure skips
	// certificate verification (for self-signed certificates).
	TLS         bool          `yaml:"tls"`
	TLSInsecure bool          `yaml:"tls_insecure"`
	Interval    time.Duration `yaml:"interval"`
	Timeout     time.Duration `yaml:"timeout"`
}

func (o Options) addr() string {
	if _, _, err := net.SplitHostPort(o.Host); err == nil {
		return o.Host
	}
	return net.JoinHostPort(strings.Trim(o.Host, "[]"), DefaultPort)
}

func (o Options) password() string {
	if o.PasswordEnv != "" {
		return os.Getenv(o.PasswordEnv)
	}
	return o.Password
}

func parseOptions(cfg sdk.Config) (Options, error) {
	o := Options{Interval: 2 * time.Second, Timeout: 5 * time.Second}
	if err := cfg.Decode(&o); err != nil {
		return o, err
	}
	var errs []error
	if o.Host == "" {
		errs = append(errs, errors.New(`host is required: the NUT server, such as "192.168.1.10" or "nas.local:3493"`))
	}
	if o.Password != "" && o.PasswordEnv != "" {
		errs = append(errs, errors.New("set either password or password_env, not both"))
	}
	if o.Username != "" && o.Password == "" && o.PasswordEnv == "" {
		errs = append(errs, errors.New("username needs password or password_env"))
	}
	if o.PasswordEnv != "" && os.Getenv(o.PasswordEnv) == "" {
		errs = append(errs, fmt.Errorf("password_env names environment variable %s, but it is not set", o.PasswordEnv))
	}
	if o.Interval < 500*time.Millisecond {
		errs = append(errs, errors.New("interval must be at least 500ms"))
	}
	if o.Timeout < time.Second {
		errs = append(errs, errors.New("timeout must be at least 1s"))
	}
	return o, errors.Join(errs...)
}

// Driver is the nut driver.
type Driver struct{}

// New returns the nut driver.
func New() *Driver { return &Driver{} }

// Info implements sdk.Driver.
func (*Driver) Info() sdk.Info {
	return sdk.Info{Name: DriverName, Kind: sdk.KindDriver, Version: Version,
		Description: "A UPS on a NUT server (another nutng, or NUT's upsd)"}
}

// Discover implements sdk.Driver.
func (*Driver) Discover(context.Context) ([]sdk.Discovered, error) { return nil, nil }

// ValidateConfig implements sdk.Driver.
func (*Driver) ValidateConfig(cfg sdk.Config) error {
	_, err := parseOptions(cfg)
	return err
}

// ProtoError is an ERR reply from the server.
type ProtoError struct{ Code string }

func (e *ProtoError) Error() string { return "server replied ERR " + e.Code }

func isCode(err error, codes ...string) bool {
	var pe *ProtoError
	if !errors.As(err, &pe) {
		return false
	}
	for _, c := range codes {
		if pe.Code == c {
			return true
		}
	}
	return false
}

// Open implements sdk.Driver.
func (*Driver) Open(ctx context.Context, cfg sdk.Config, log *slog.Logger) (sdk.Device, error) {
	o, err := parseOptions(cfg)
	if err != nil {
		return nil, err
	}
	c, err := dial(ctx, o)
	if err != nil {
		return nil, err
	}
	d := &device{c: c, o: o, log: log}
	if err := d.setup(); err != nil {
		c.close()
		return nil, err
	}
	return d, nil
}

// conn is one NUT protocol connection; requests are serialized.
type conn struct {
	mu      sync.Mutex
	nc      net.Conn
	r       *bufio.Reader
	timeout time.Duration
}

func dial(ctx context.Context, o Options) (*conn, error) {
	d := net.Dialer{Timeout: o.Timeout}
	nc, err := d.DialContext(ctx, "tcp", o.addr())
	if err != nil {
		return nil, fmt.Errorf("connect to NUT server %s: %w", o.addr(), err)
	}
	c := &conn{nc: nc, r: bufio.NewReader(nc), timeout: o.Timeout}
	if o.TLS {
		if _, err := c.cmd("STARTTLS"); err != nil {
			nc.Close()
			return nil, fmt.Errorf("STARTTLS with %s: %w", o.addr(), err)
		}
		host, _, _ := net.SplitHostPort(o.addr())
		tc := tls.Client(nc, &tls.Config{ServerName: host, InsecureSkipVerify: o.TLSInsecure, MinVersion: tls.VersionTLS12})
		_ = tc.SetDeadline(time.Now().Add(o.Timeout))
		if err := tc.Handshake(); err != nil {
			nc.Close()
			return nil, fmt.Errorf("TLS with %s: %w (use tls_insecure: true for a self-signed certificate)", o.addr(), err)
		}
		_ = tc.SetDeadline(time.Time{})
		c.nc, c.r = tc, bufio.NewReader(tc)
	}
	return c, nil
}

func (c *conn) send(line string) error {
	_ = c.nc.SetDeadline(time.Now().Add(c.timeout))
	_, err := fmt.Fprintf(c.nc, "%s\n", line)
	return err
}

func (c *conn) readLine() (string, error) {
	_ = c.nc.SetDeadline(time.Now().Add(c.timeout))
	s, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// cmd sends a command and returns its one-line reply.
func (c *conn) cmd(line string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.send(line); err != nil {
		return "", err
	}
	reply, err := c.readLine()
	if err != nil {
		return "", err
	}
	if code, ok := strings.CutPrefix(reply, "ERR "); ok {
		code, _, _ = strings.Cut(code, " ")
		return "", &ProtoError{Code: code}
	}
	return reply, nil
}

// list sends a LIST command and returns the tokenized lines between BEGIN
// and END.
func (c *conn) list(line string) ([][]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.send(line); err != nil {
		return nil, err
	}
	first, err := c.readLine()
	if err != nil {
		return nil, err
	}
	if code, ok := strings.CutPrefix(first, "ERR "); ok {
		code, _, _ = strings.Cut(code, " ")
		return nil, &ProtoError{Code: code}
	}
	if !strings.HasPrefix(first, "BEGIN LIST") {
		return nil, fmt.Errorf("unexpected reply %q to %s", first, line)
	}
	var out [][]string
	for {
		l, err := c.readLine()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(l, "END LIST") {
			return out, nil
		}
		f, err := nutwire.Split(l)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
}

func (c *conn) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.send("LOGOUT")
	c.nc.Close()
}

// device is one UPS on a NUT server.
type device struct {
	c        *conn
	o        Options
	log      *slog.Logger
	ups      string
	commands []sdk.Command
	writable []string
	seen     map[string]bool
	stale    bool
	once     sync.Once
}

func (d *device) setup() error {
	d.ups = d.o.UPS
	if d.ups == "" {
		lines, err := d.c.list("LIST UPS")
		if err != nil {
			return fmt.Errorf("list UPSes on %s: %w", d.o.addr(), err)
		}
		var names []string
		for _, f := range lines {
			if len(f) >= 2 && f[0] == "UPS" {
				names = append(names, f[1])
			}
		}
		switch {
		case d.ups == "" && len(names) == 1:
			d.ups = names[0]
		case d.ups == "":
			return fmt.Errorf("%s has %d UPSes (%s); set `ups:` to choose one", d.o.addr(), len(names), strings.Join(names, ", "))
		}
	}
	if d.o.Username != "" {
		if _, err := d.c.cmd("USERNAME " + nutwire.Quote(d.o.Username)); err != nil {
			return fmt.Errorf("log in to %s: %w", d.o.addr(), err)
		}
		if _, err := d.c.cmd("PASSWORD " + nutwire.Quote(d.o.password())); err != nil {
			return fmt.Errorf("log in to %s: %w", d.o.addr(), err)
		}
		if _, err := d.c.cmd("LOGIN " + d.ups); err != nil {
			switch {
			case isCode(err, "UNKNOWN-UPS"):
				return d.unknownUPS()
			case isCode(err, "ACCESS-DENIED"):
				return fmt.Errorf("log in to %s as %s: wrong password, or the user may not monitor UPS %s", d.o.addr(), d.o.Username, d.ups)
			}
			return fmt.Errorf("log in to %s: %w", d.o.addr(), err)
		}
	}

	cmds, err := d.c.list("LIST CMD " + d.ups)
	if isCode(err, "UNKNOWN-UPS") {
		return d.unknownUPS()
	}
	if err != nil {
		return fmt.Errorf("list commands of %s on %s: %w", d.ups, d.o.addr(), err)
	}
	for _, f := range cmds {
		if len(f) >= 3 {
			d.commands = append(d.commands, sdk.NewCommand(f[2]))
		}
	}
	if rw, err := d.c.list("LIST RW " + d.ups); err == nil {
		for _, f := range rw {
			if len(f) >= 3 {
				d.writable = append(d.writable, f[2])
			}
		}
	}
	d.seen = map[string]bool{}
	d.log.Info("connected to NUT server", "server", d.o.addr(), "ups", d.ups, "logged_in", d.o.Username != "")
	return nil
}

func (d *device) unknownUPS() error {
	var names []string
	if lines, err := d.c.list("LIST UPS"); err == nil {
		for _, f := range lines {
			if len(f) >= 2 {
				names = append(names, f[1])
			}
		}
	}
	return fmt.Errorf("%s has no UPS named %q%s (it has: %s)", d.o.addr(), d.ups,
		suggest.DidYouMean(d.ups, names), strings.Join(names, ", "))
}

func (d *device) Watch(ctx context.Context, sink func(sdk.Update)) error {
	t := time.NewTicker(d.o.Interval)
	defer t.Stop()
	for {
		lines, err := d.c.list("LIST VAR " + d.ups)
		switch {
		case isCode(err, "DATA-STALE", "DRIVER-NOT-CONNECTED"):
			// The server has no fresh data; report nothing, so the UPS
			// turns stale here too.
			if !d.stale {
				d.log.Warn("NUT server has no current data for the UPS", "server", d.o.addr(), "ups", d.ups, "reply", err)
				d.stale = true
			}
		case err != nil:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read %s from %s: %w", d.ups, d.o.addr(), err)
		default:
			d.stale = false
			vars := map[string]string{}
			for _, f := range lines {
				if len(f) >= 4 && f[0] == "VAR" {
					vars[f[2]] = f[3]
				}
			}
			var removed []string
			for k := range d.seen {
				if _, ok := vars[k]; !ok {
					removed = append(removed, k)
					delete(d.seen, k)
				}
			}
			for k := range vars {
				d.seen[k] = true
			}
			sink(sdk.Update{Vars: vars, Removed: removed})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (d *device) Commands() []sdk.Command { return d.commands }
func (d *device) Writable() []string      { return d.writable }

func (d *device) RunCommand(_ context.Context, name, param string) error {
	line := "INSTCMD " + d.ups + " " + name
	if param != "" {
		line += " " + nutwire.Quote(param)
	}
	return d.mapErr(name, func() error { _, err := d.c.cmd(line); return err })
}

func (d *device) SetVar(_ context.Context, name, value string) error {
	return d.mapErr(name, func() error {
		_, err := d.c.cmd("SET VAR " + d.ups + " " + name + " " + nutwire.Quote(value))
		return err
	})
}

func (d *device) mapErr(what string, fn func() error) error {
	err := fn()
	switch {
	case err == nil:
		return nil
	case isCode(err, "ACCESS-DENIED", "USERNAME-REQUIRED", "PASSWORD-REQUIRED"):
		return fmt.Errorf("%s: the NUT server refused (%v); the username in nutng.yaml needs admin rights there: %w", what, err, sdk.ErrNotSupported)
	case isCode(err, "CMD-NOT-SUPPORTED", "VAR-NOT-SUPPORTED", "READONLY"):
		return fmt.Errorf("%s: %v: %w", what, err, sdk.ErrNotSupported)
	case isCode(err, "INVALID-VALUE", "INVALID-ARGUMENT"):
		return fmt.Errorf("%s: %v: %w", what, err, sdk.ErrInvalid)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (d *device) Close() error {
	d.once.Do(d.c.close)
	return nil
}
