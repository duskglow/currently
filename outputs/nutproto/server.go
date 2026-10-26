// Package nutproto serves UPS state over the Network UPS Tools protocol
// (the upsd network protocol, version 1.3), so NUT clients such as upsc,
// upsmon, Home Assistant, Synology DSM and Nagios check_ups work with
// nutng unchanged.
package nutproto

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// ProtocolVersion is the NUT network protocol version implemented.
const ProtocolVersion = "1.3"

// Roles, matching config.Role*.
const (
	RoleSecondary = "secondary"
	RolePrimary   = "primary"
	RoleAdmin     = "admin"
)

// User is an account allowed to log in.
type User struct {
	Password string
	Role     string
}

// Options configure the server.
type Options struct {
	Users   map[string]User
	TLS     *tls.Config // enables STARTTLS
	Version string
	Log     *slog.Logger
}

// Server is a NUT protocol server.
type Server struct {
	host sdk.Host
	opts Options

	mu     sync.Mutex
	conns  map[*conn]struct{}
	logins map[string]map[*conn]struct{} // UPS → clients logged in to it
}

const (
	maxLine  = 1024
	maxConns = 512
)

// New returns a server.
func New(host sdk.Host, opts Options) *Server {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Server{host: host, opts: opts, conns: map[*conn]struct{}{}, logins: map[string]map[*conn]struct{}{}}
}

// Serve accepts connections on ln until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			c.nc.Close()
		}
		s.mu.Unlock()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return err
		}
		c := &conn{s: s, nc: nc, addr: remoteIP(nc)}
		s.mu.Lock()
		if len(s.conns) >= maxConns {
			s.mu.Unlock()
			fmt.Fprint(nc, "ERR MAX-CLIENTS\n")
			nc.Close()
			continue
		}
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.serve()
			s.drop(c)
		}()
	}
}

func (s *Server) drop(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, c)
	if c.login != "" {
		delete(s.logins[c.login], c)
	}
	c.nc.Close()
}

func remoteIP(nc net.Conn) string {
	host, _, err := net.SplitHostPort(nc.RemoteAddr().String())
	if err != nil {
		return nc.RemoteAddr().String()
	}
	return host
}

// conn is one client connection.
type conn struct {
	s    *Server
	nc   net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	addr string

	username, password string
	login              string // UPS this client logged in to
	tls                bool
}

func (c *conn) serve() {
	c.r = bufio.NewReaderSize(c.nc, maxLine)
	c.w = bufio.NewWriter(c.nc)
	for {
		line, err := c.r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			c.send("ERR TOO-LONG")
			c.w.Flush()
			return
		}
		if err != nil {
			return
		}
		args, err := split(strings.TrimRight(string(line), "\r\n"))
		switch {
		case err != nil:
			c.send("ERR INVALID-ARGUMENT")
		case len(args) > 0:
			if done := c.handle(args); done {
				c.w.Flush()
				return
			}
		}
		if err := c.w.Flush(); err != nil {
			return
		}
	}
}

func (c *conn) send(format string, args ...any) {
	fmt.Fprintf(c.w, format+"\n", args...)
}

// handle runs one command and reports whether to close the connection.
func (c *conn) handle(args []string) bool {
	cmd := strings.ToUpper(args[0])
	rest := args[1:]
	switch cmd {
	case "VER":
		c.send("Network UPS Tools upsd 2.8.1 compatible - nut-ng %s", c.s.opts.Version)
	case "NETVER", "PROTVER":
		c.send(ProtocolVersion)
	case "HELP":
		c.send("Commands: HELP VER GET LIST SET INSTCMD LOGIN LOGOUT USERNAME PASSWORD STARTTLS")
	case "USERNAME":
		switch {
		case len(rest) != 1:
			c.send("ERR INVALID-ARGUMENT")
		case c.username != "":
			c.send("ERR ALREADY-SET-USERNAME")
		default:
			c.username = rest[0]
			c.send("OK")
		}
	case "PASSWORD":
		switch {
		case len(rest) != 1:
			c.send("ERR INVALID-ARGUMENT")
		case c.password != "":
			c.send("ERR ALREADY-SET-PASSWORD")
		default:
			c.password = rest[0]
			c.send("OK")
		}
	case "LOGIN":
		c.loginCmd(rest)
	case "PRIMARY", "MASTER":
		if len(rest) != 1 {
			c.send("ERR INVALID-ARGUMENT")
		} else if c.require(rest[0], RolePrimary) {
			c.s.opts.Log.Info("NUT client is primary", "client", c.addr, "user", c.username, "ups", rest[0])
			c.send("OK %s-GRANTED", cmd)
		}
	case "FSD":
		if len(rest) != 1 {
			c.send("ERR INVALID-ARGUMENT")
		} else if c.require(rest[0], RolePrimary) {
			if err := c.s.host.SetForcedShutdown(context.Background(), rest[0]); err != nil {
				c.sendErr(err, "")
			} else {
				c.s.opts.Log.Warn("NUT client set forced shutdown", "client", c.addr, "user", c.username, "ups", rest[0])
				c.send("OK FSD-SET")
			}
		}
	case "LOGOUT":
		c.send("OK Goodbye")
		return true
	case "STARTTLS":
		c.startTLS()
	case "GET":
		c.get(rest)
	case "LIST":
		c.list(rest)
	case "SET":
		c.set(rest)
	case "INSTCMD":
		c.instcmd(rest)
	default:
		c.send("ERR UNKNOWN-COMMAND")
	}
	return false
}

// user returns the authenticated user's role, or "" when the credentials
// are missing or wrong.
func (c *conn) role() string {
	u, ok := c.s.opts.Users[c.username]
	if !ok || u.Password == "" || subtle.ConstantTimeCompare([]byte(u.Password), []byte(c.password)) != 1 {
		return ""
	}
	return u.Role
}

var roleRank = map[string]int{RoleSecondary: 1, RolePrimary: 2, RoleAdmin: 3}

// require checks credentials and role for an action on a UPS, sending the
// error if not allowed.
func (c *conn) require(ups, role string) bool {
	switch {
	case c.username == "":
		c.send("ERR USERNAME-REQUIRED")
	case c.password == "":
		c.send("ERR PASSWORD-REQUIRED")
	case !c.exists(ups):
		c.send("ERR UNKNOWN-UPS")
	case roleRank[c.role()] < roleRank[role]:
		c.s.opts.Log.Warn("NUT client denied", "client", c.addr, "user", c.username, "ups", ups, "needs", role)
		c.send("ERR ACCESS-DENIED")
	default:
		return true
	}
	return false
}

func (c *conn) exists(ups string) bool {
	_, ok := c.s.host.GetUPS(ups)
	return ok
}

func (c *conn) loginCmd(rest []string) {
	if len(rest) != 1 {
		c.send("ERR INVALID-ARGUMENT")
		return
	}
	if c.login != "" {
		c.send("ERR ALREADY-LOGGED-IN")
		return
	}
	if !c.require(rest[0], RoleSecondary) {
		return
	}
	c.s.mu.Lock()
	if c.s.logins[rest[0]] == nil {
		c.s.logins[rest[0]] = map[*conn]struct{}{}
	}
	c.s.logins[rest[0]][c] = struct{}{}
	c.s.mu.Unlock()
	c.login = rest[0]
	c.s.opts.Log.Info("NUT client logged in", "client", c.addr, "user", c.username, "ups", rest[0])
	c.send("OK")
}

func (c *conn) startTLS() {
	switch {
	case c.tls:
		c.send("ERR ALREADY-SSL-MODE")
	case c.s.opts.TLS == nil:
		c.send("ERR FEATURE-NOT-CONFIGURED")
	default:
		c.send("OK STARTTLS")
		if c.w.Flush() != nil {
			return
		}
		tc := tls.Server(c.nc, c.s.opts.TLS)
		if err := tc.Handshake(); err != nil {
			c.s.opts.Log.Warn("NUT TLS handshake failed", "client", c.addr, "error", err)
			c.nc.Close()
			return
		}
		c.s.mu.Lock() // Serve's shutdown closes c.nc
		c.nc, c.tls = tc, true
		c.s.mu.Unlock()
		c.r = bufio.NewReaderSize(tc, maxLine)
		c.w = bufio.NewWriter(tc)
	}
}

// snapshot returns a UPS whose data may be served, or sends an error.
func (c *conn) snapshot(name string) (sdk.UPS, bool) {
	u, ok := c.s.host.GetUPS(name)
	switch {
	case !ok:
		c.send("ERR UNKNOWN-UPS")
	case u.State == sdk.StateStale:
		c.send("ERR DATA-STALE")
	case u.State != sdk.StateConnected:
		c.send("ERR DRIVER-NOT-CONNECTED")
	default:
		return u, true
	}
	return u, false
}

func (c *conn) get(args []string) {
	if len(args) < 2 {
		c.send("ERR INVALID-ARGUMENT")
		return
	}
	what, ups := strings.ToUpper(args[0]), args[1]
	switch what {
	case "NUMLOGINS":
		if !c.exists(ups) {
			c.send("ERR UNKNOWN-UPS")
			return
		}
		c.s.mu.Lock()
		n := len(c.s.logins[ups])
		c.s.mu.Unlock()
		c.send("NUMLOGINS %s %d", ups, n)
	case "UPSDESC":
		u, ok := c.s.host.GetUPS(ups)
		if !ok {
			c.send("ERR UNKNOWN-UPS")
			return
		}
		c.send("UPSDESC %s %s", ups, quote(description(u)))
	case "VAR", "TYPE", "DESC":
		if len(args) != 3 {
			c.send("ERR INVALID-ARGUMENT")
			return
		}
		name := args[2]
		u, ok := c.snapshot(ups)
		if !ok {
			return
		}
		v, ok := u.Vars[name]
		if !ok {
			c.send("ERR VAR-NOT-SUPPORTED")
			return
		}
		switch what {
		case "VAR":
			c.send("VAR %s %s %s", ups, name, quote(v.Value))
		case "TYPE":
			c.send("TYPE %s %s %s", ups, name, varType(v))
		case "DESC":
			desc := "Description unavailable"
			if spec, ok := sdk.LookupVar(name); ok {
				desc = spec.Description
			}
			c.send("DESC %s %s %s", ups, name, quote(desc))
		}
	case "CMDDESC":
		if len(args) != 3 {
			c.send("ERR INVALID-ARGUMENT")
			return
		}
		u, ok := c.s.host.GetUPS(ups)
		if !ok {
			c.send("ERR UNKNOWN-UPS")
			return
		}
		for _, cmd := range u.Commands {
			if cmd.Name == args[2] {
				desc := cmd.Description
				if desc == "" {
					desc = "Description unavailable"
				}
				c.send("CMDDESC %s %s %s", ups, cmd.Name, quote(desc))
				return
			}
		}
		c.send("ERR CMD-NOT-SUPPORTED")
	default:
		c.send("ERR INVALID-ARGUMENT")
	}
}

func varType(v sdk.Variable) string {
	numeric := false
	if spec, ok := sdk.LookupVar(v.Name); ok {
		numeric = spec.Numeric
	} else {
		_, numeric = v.Float()
	}
	t := "STRING:" + strconv.Itoa(max(64, len(v.Value)))
	if numeric {
		t = "NUMBER"
	}
	if v.Writable {
		return "RW " + t
	}
	return t
}

func description(u sdk.UPS) string {
	switch {
	case u.Description != "":
		return u.Description
	case u.Model() != "":
		return u.Model()
	}
	return "Description unavailable"
}

func (c *conn) list(args []string) {
	if len(args) == 0 {
		c.send("ERR INVALID-ARGUMENT")
		return
	}
	what := strings.ToUpper(args[0])
	if what == "UPS" {
		c.send("BEGIN LIST UPS")
		for _, u := range c.s.host.ListUPS() {
			c.send("UPS %s %s", u.Name, quote(description(u)))
		}
		c.send("END LIST UPS")
		return
	}
	if len(args) < 2 {
		c.send("ERR INVALID-ARGUMENT")
		return
	}
	ups := args[1]
	switch what {
	case "VAR", "RW":
		u, ok := c.snapshot(ups)
		if !ok {
			return
		}
		names := make([]string, 0, len(u.Vars))
		for k, v := range u.Vars {
			if what == "VAR" || v.Writable {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		c.send("BEGIN LIST %s %s", what, ups)
		for _, k := range names {
			c.send("%s %s %s %s", what, ups, k, quote(u.Vars[k].Value))
		}
		c.send("END LIST %s %s", what, ups)
	case "CMD":
		u, ok := c.s.host.GetUPS(ups)
		if !ok {
			c.send("ERR UNKNOWN-UPS")
			return
		}
		c.send("BEGIN LIST CMD %s", ups)
		for _, cmd := range u.Commands {
			c.send("CMD %s %s", ups, cmd.Name)
		}
		c.send("END LIST CMD %s", ups)
	case "ENUM", "RANGE":
		if len(args) != 3 {
			c.send("ERR INVALID-ARGUMENT")
			return
		}
		u, ok := c.snapshot(ups)
		if !ok {
			return
		}
		if _, ok := u.Vars[args[2]]; !ok {
			c.send("ERR VAR-NOT-SUPPORTED")
			return
		}
		c.send("BEGIN LIST %s %s %s", what, ups, args[2])
		c.send("END LIST %s %s %s", what, ups, args[2])
	case "CLIENT":
		if !c.exists(ups) {
			c.send("ERR UNKNOWN-UPS")
			return
		}
		c.s.mu.Lock()
		var addrs []string
		for cl := range c.s.logins[ups] {
			addrs = append(addrs, cl.addr)
		}
		c.s.mu.Unlock()
		sort.Strings(addrs)
		c.send("BEGIN LIST CLIENT %s", ups)
		for _, a := range addrs {
			c.send("CLIENT %s %s", ups, a)
		}
		c.send("END LIST CLIENT %s", ups)
	default:
		c.send("ERR INVALID-ARGUMENT")
	}
}

func (c *conn) set(args []string) {
	// SET VAR <ups> <var> "<value>"
	if len(args) != 4 || !strings.EqualFold(args[0], "VAR") {
		c.send("ERR INVALID-ARGUMENT")
		return
	}
	ups, name, value := args[1], args[2], args[3]
	if !c.require(ups, RoleAdmin) {
		return
	}
	u, _ := c.s.host.GetUPS(ups)
	v, ok := u.Vars[name]
	switch {
	case !ok:
		c.send("ERR VAR-NOT-SUPPORTED")
		return
	case !v.Writable:
		c.send("ERR READONLY")
		return
	}
	if err := c.s.host.SetVar(context.Background(), ups, name, value); err != nil {
		c.sendErr(err, "SET-FAILED")
		return
	}
	c.s.opts.Log.Info("NUT client set variable", "client", c.addr, "user", c.username, "ups", ups, "variable", name, "value", value)
	c.send("OK")
}

func (c *conn) instcmd(args []string) {
	// INSTCMD <ups> <cmd> [<value>]
	if len(args) < 2 || len(args) > 3 {
		c.send("ERR INVALID-ARGUMENT")
		return
	}
	ups, name := args[0], args[1]
	param := ""
	if len(args) == 3 {
		param = args[2]
	}
	if !c.require(ups, RoleAdmin) {
		return
	}
	u, _ := c.s.host.GetUPS(ups)
	if !slices.ContainsFunc(u.Commands, func(x sdk.Command) bool { return x.Name == name }) {
		c.send("ERR CMD-NOT-SUPPORTED")
		return
	}
	if err := c.s.host.RunCommand(context.Background(), ups, name, param); err != nil {
		c.sendErr(err, "INSTCMD-FAILED")
		return
	}
	c.s.opts.Log.Info("NUT client ran command", "client", c.addr, "user", c.username, "ups", ups, "command", name)
	c.send("OK")
}

// sendErr maps an sdk error to a NUT error code.
func (c *conn) sendErr(err error, fallback string) {
	switch {
	case errors.Is(err, sdk.ErrNotConnected):
		c.send("ERR DRIVER-NOT-CONNECTED")
	case errors.Is(err, sdk.ErrInvalid):
		c.send("ERR INVALID-VALUE")
	case errors.Is(err, sdk.ErrNotSupported):
		c.send("ERR CMD-NOT-SUPPORTED")
	case errors.Is(err, sdk.ErrNotFound):
		c.send("ERR UNKNOWN-UPS")
	case fallback != "":
		c.s.opts.Log.Warn("NUT request failed", "client", c.addr, "error", err)
		c.send("ERR %s", fallback)
	default:
		c.send("ERR UNKNOWN-COMMAND")
	}
}
