package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/duskglow/currently/pkg/sdk"
)

// Version of the plugin.
const Version = "0.1.0"

// Driver reads UPSes through their SNMP network cards.
type Driver struct{}

// Info implements sdk.Driver.
func (Driver) Info() sdk.Info {
	return sdk.Info{Name: "snmp", Kind: sdk.KindDriver, Version: Version,
		Description: "UPS network cards over SNMP (UPS-MIB, APC PowerNet-MIB)"}
}

// Options configure one UPS.
type Options struct {
	// Host is "host" or "host:port" (port 161 by default).
	Host string `yaml:"host"`
	// Version is "1", "2c" (default) or "3".
	Version      string `yaml:"version"`
	Community    string `yaml:"community"`
	CommunityEnv string `yaml:"community_env"`
	// WriteCommunity enables instant commands with v1/v2c. With v3, the
	// same user reads and writes.
	WriteCommunity    string `yaml:"write_community"`
	WriteCommunityEnv string `yaml:"write_community_env"`
	// SNMPv3.
	Username           string `yaml:"username"`
	Auth               string `yaml:"auth"`
	AuthPassword       string `yaml:"auth_password"`
	AuthPasswordEnv    string `yaml:"auth_password_env"`
	Privacy            string `yaml:"privacy"`
	PrivacyPassword    string `yaml:"privacy_password"`
	PrivacyPasswordEnv string `yaml:"privacy_password_env"`
	Context            string `yaml:"context"`
	// MIB is "auto" (default), "ietf" (UPS-MIB, RFC 1628) or "apc".
	MIB      string        `yaml:"mib"`
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
	Retries  *int          `yaml:"retries"`
}

var (
	authProtocols = map[string]gosnmp.SnmpV3AuthProtocol{"none": gosnmp.NoAuth, "md5": gosnmp.MD5,
		"sha": gosnmp.SHA, "sha224": gosnmp.SHA224, "sha256": gosnmp.SHA256, "sha384": gosnmp.SHA384,
		"sha512": gosnmp.SHA512}
	// Old cards often support only DES, so the client accepts it.
	privacyProtocols = map[string]gosnmp.SnmpV3PrivProtocol{"none": gosnmp.NoPriv, "des": gosnmp.DES,
		"aes": gosnmp.AES, "aes192": gosnmp.AES192, "aes256": gosnmp.AES256,
		"aes192c": gosnmp.AES192C, "aes256c": gosnmp.AES256C}
)

func secret(literal, env string) string {
	if env != "" {
		return os.Getenv(env)
	}
	return literal
}

func normalize(s, def string) string {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
	switch s {
	case "":
		return def
	case "sha1":
		return "sha"
	case "aes128":
		return "aes"
	}
	return s
}

func parseOptions(cfg sdk.Config) (Options, error) {
	o := Options{Version: "2c", MIB: "auto", Interval: 10 * time.Second, Timeout: 3 * time.Second}
	if err := cfg.Decode(&o); err != nil {
		return o, err
	}
	var errs []error
	if strings.TrimSpace(o.Host) == "" {
		errs = append(errs, errors.New("host is required (the UPS card's address)"))
	}
	o.Version = strings.TrimPrefix(strings.ToLower(o.Version), "v")
	switch o.Version {
	case "1", "2c":
		if secret(o.Community, o.CommunityEnv) == "" && o.Community == "" && o.CommunityEnv == "" {
			o.Community = "public"
		}
	case "2":
		o.Version = "2c"
	case "3":
		if o.Username == "" {
			errs = append(errs, errors.New("username is required with version 3"))
		}
		auth, priv := normalize(o.Auth, "none"), normalize(o.Privacy, "none")
		if _, ok := authProtocols[auth]; !ok {
			errs = append(errs, fmt.Errorf("auth %q is not one of none, md5, sha, sha224, sha256, sha384, sha512", o.Auth))
		}
		if _, ok := privacyProtocols[priv]; !ok {
			errs = append(errs, fmt.Errorf("privacy %q is not one of none, des, aes, aes192, aes256, aes192c, aes256c", o.Privacy))
		}
		if priv != "none" && auth == "none" {
			errs = append(errs, errors.New("privacy needs an auth protocol as well"))
		}
		if auth != "none" && secret(o.AuthPassword, o.AuthPasswordEnv) == "" {
			errs = append(errs, errors.New("auth_password or auth_password_env is required"))
		}
		if priv != "none" && secret(o.PrivacyPassword, o.PrivacyPasswordEnv) == "" {
			errs = append(errs, errors.New("privacy_password or privacy_password_env is required"))
		}
	default:
		errs = append(errs, fmt.Errorf("version %q is not 1, 2c or 3", o.Version))
	}
	if o.MIB = strings.ToLower(o.MIB); o.MIB != "auto" && mibs[o.MIB] == nil {
		errs = append(errs, fmt.Errorf("mib %q is not auto, ietf or apc", o.MIB))
	}
	if o.Interval < time.Second {
		errs = append(errs, errors.New("interval must be at least 1s"))
	}
	if o.Timeout <= 0 {
		errs = append(errs, errors.New("timeout must be positive"))
	}
	return o, errors.Join(errs...)
}

// ValidateConfig implements sdk.Driver.
func (Driver) ValidateConfig(cfg sdk.Config) error {
	_, err := parseOptions(cfg)
	return err
}

// Discover implements sdk.Driver. Network cards are configured by address.
func (Driver) Discover(context.Context) ([]sdk.Discovered, error) { return nil, nil }

// client builds a session; write selects the write community.
func (o Options) client(write bool) (*gosnmp.GoSNMP, error) {
	host, port := o.Host, uint16(161)
	if h, p, err := net.SplitHostPort(o.Host); err == nil {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("bad port in host %q", o.Host)
		}
		host, port = h, uint16(n)
	}
	retries := 2
	if o.Retries != nil {
		retries = *o.Retries
	}
	g := &gosnmp.GoSNMP{Target: strings.Trim(host, "[]"), Port: port, Timeout: o.Timeout, Retries: retries,
		MaxOids: 30, Community: secret(o.Community, o.CommunityEnv), ContextName: o.Context}
	switch o.Version {
	case "1":
		g.Version = gosnmp.Version1
	case "2c":
		g.Version = gosnmp.Version2c
	case "3":
		g.Version, g.SecurityModel = gosnmp.Version3, gosnmp.UserSecurityModel
		auth, priv := normalize(o.Auth, "none"), normalize(o.Privacy, "none")
		g.MsgFlags = gosnmp.NoAuthNoPriv
		if auth != "none" {
			g.MsgFlags = gosnmp.AuthNoPriv
		}
		if priv != "none" {
			g.MsgFlags = gosnmp.AuthPriv
		}
		g.SecurityParameters = &gosnmp.UsmSecurityParameters{UserName: o.Username,
			AuthenticationProtocol: authProtocols[auth], AuthenticationPassphrase: secret(o.AuthPassword, o.AuthPasswordEnv),
			PrivacyProtocol: privacyProtocols[priv], PrivacyPassphrase: secret(o.PrivacyPassword, o.PrivacyPasswordEnv)}
	}
	if write && o.Version != "3" {
		g.Community = secret(o.WriteCommunity, o.WriteCommunityEnv)
	}
	if err := g.Connect(); err != nil {
		return nil, err
	}
	return g, nil
}

// canWrite reports whether commands can be sent.
func (o Options) canWrite() bool {
	return o.Version == "3" || secret(o.WriteCommunity, o.WriteCommunityEnv) != ""
}

const (
	oidSysDescr    = ".1.3.6.1.2.1.1.1.0"
	oidSysObjectID = ".1.3.6.1.2.1.1.2.0"
)

// Open implements sdk.Driver.
func (Driver) Open(ctx context.Context, cfg sdk.Config, log *slog.Logger) (sdk.Device, error) {
	o, err := parseOptions(cfg)
	if err != nil {
		return nil, err
	}
	g, err := o.client(false)
	if err != nil {
		return nil, err
	}
	d := &device{o: o, g: g, log: log, missing: map[string]bool{}, poke: make(chan struct{}, 1)}
	sys, err := d.get(ctx, []string{oidSysDescr, oidSysObjectID})
	if err != nil {
		g.Conn.Close()
		return nil, fmt.Errorf("no SNMP answer from %s: %w", o.Host, err)
	}
	d.sysDescr, _ = sys[oidSysDescr].(string)
	objectID, _ := sys[oidSysObjectID].(string)
	if d.m, err = d.pickMIB(ctx, objectID); err != nil {
		g.Conn.Close()
		return nil, err
	}
	log.Info("SNMP UPS connected", "host", o.Host, "mib", d.m.name, "sysDescr", d.sysDescr)
	return d, nil
}

// pickMIB chooses the MIB: APC's for APC cards, otherwise the UPS-MIB.
func (d *device) pickMIB(ctx context.Context, objectID string) (*mib, error) {
	if d.o.MIB != "auto" {
		return mibs[d.o.MIB], nil
	}
	if strings.HasPrefix(objectID, ".1.3.6.1.4.1.318.") {
		return apcMIB, nil
	}
	probe := []string{ietf + ".1.2.0", ietf + ".4.1.0"}
	got, err := d.get(ctx, probe)
	if err == nil && len(got) > 0 {
		return ietfMIB, nil
	}
	got, err = d.get(ctx, []string{apc + ".1.1.1.0"})
	if err == nil && len(got) > 0 {
		return apcMIB, nil
	}
	return nil, fmt.Errorf("%s answers SNMP (%s) but serves neither the UPS-MIB nor APC's PowerNet-MIB", d.o.Host, objectID)
}

type device struct {
	o        Options
	log      *slog.Logger
	m        *mib
	sysDescr string
	poke     chan struct{}

	mu      sync.Mutex
	g       *gosnmp.GoSNMP
	w       *gosnmp.GoSNMP  // write session (v1/v2c write community)
	missing map[string]bool // objects a v1 agent does not have
	last    values
}

// get reads objects. Missing objects are left out. SNMPv1 agents fail a
// whole request for one missing object, so on noSuchName the objects are
// read one by one and the missing ones remembered.
func (d *device) get(ctx context.Context, oids []string) (values, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := values{}
	var want []string
	for _, o := range oids {
		if !d.missing[o] {
			want = append(want, o)
		}
	}
	for chunk := range slices.Chunk(want, d.g.MaxOids) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		r, err := d.g.Get(chunk)
		if err != nil {
			return nil, err
		}
		if r.Error == gosnmp.NoSuchName && d.g.Version == gosnmp.Version1 {
			for _, o := range chunk {
				r1, err := d.g.Get([]string{o})
				if err != nil {
					return nil, err
				}
				if r1.Error == gosnmp.NoSuchName {
					d.missing[o] = true
					continue
				}
				collect(out, r1.Variables)
			}
			continue
		}
		if r.Error != gosnmp.NoError {
			return nil, fmt.Errorf("SNMP error %s", r.Error)
		}
		collect(out, r.Variables)
	}
	return out, nil
}

func collect(out values, vbs []gosnmp.SnmpPDU) {
	for _, v := range vbs {
		name := v.Name
		if !strings.HasPrefix(name, ".") {
			name = "." + name
		}
		switch v.Type {
		case gosnmp.OctetString:
			b, _ := v.Value.([]byte)
			out[name] = strings.TrimRight(strings.TrimSpace(string(b)), "\x00")
		case gosnmp.ObjectIdentifier:
			s, _ := v.Value.(string)
			out[name] = s
		case gosnmp.Integer:
			if n, ok := v.Value.(int); ok {
				out[name] = int64(n)
			}
		case gosnmp.Counter32, gosnmp.Gauge32, gosnmp.TimeTicks, gosnmp.Counter64, gosnmp.Uinteger32:
			out[name] = gosnmp.ToBigInt(v.Value).Int64()
		}
	}
}

// walk reads a subtree (the alarm table).
func (d *device) walk(prefix string) (values, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var pdus []gosnmp.SnmpPDU
	var err error
	if d.g.Version == gosnmp.Version1 {
		pdus, err = d.g.WalkAll(prefix)
	} else {
		pdus, err = d.g.BulkWalkAll(prefix)
	}
	if err != nil {
		return nil, err
	}
	out := values{}
	collect(out, pdus)
	return out, nil
}

// poll reads every object of the MIB and converts them to variables.
func (d *device) poll(ctx context.Context) (map[string]string, error) {
	oids := make([]string, 0, len(d.m.fields))
	for _, f := range d.m.fields {
		oids = append(oids, f.oid)
	}
	raw, err := d.get(ctx, oids)
	if err != nil {
		return nil, err
	}
	if d.m == ietfMIB {
		if n, _ := raw.int(ietf + ".6.1.0"); n > 0 {
			alarms, err := d.walk(ietfAlarmDescr)
			if err != nil {
				return nil, err
			}
			for k, v := range alarms {
				raw[k] = v
			}
		}
	}
	vars := convert(d.m, raw)
	d.mu.Lock()
	d.last = raw
	d.mu.Unlock()
	return vars, nil
}

// convert maps raw values to NUT variables.
func convert(m *mib, raw values) map[string]string {
	vars := map[string]string{"device.type": "ups"}
	for _, f := range m.fields {
		v, ok := raw[f.oid]
		if !ok || f.name == "" {
			continue
		}
		switch f.kind {
		case kindString:
			if s, _ := v.(string); s != "" {
				vars[f.name] = s
			}
		case kindEnum:
			if n, ok := raw.int(f.oid); ok {
				if s, ok := f.enum[n]; ok {
					vars[f.name] = s
				}
			}
		default:
			n, ok := raw.int(f.oid)
			if !ok || n < 0 && f.kind != kindNumber {
				continue
			}
			x := float64(n)
			switch f.kind {
			case kindTicks:
				x /= 100
			case kindMinutes:
				x *= 60
			}
			if f.div > 0 {
				x /= f.div
			}
			vars[f.name] = strconv.FormatFloat(x, 'f', -1, 64)
		}
	}
	if m.status != nil {
		m.status(raw, vars)
	}
	if mfr := vars["device.mfr"]; mfr != "" {
		vars["ups.mfr"] = mfr
	}
	if model := vars["device.model"]; model != "" {
		vars["ups.model"] = model
	}
	return vars
}

// Watch implements sdk.Device.
func (d *device) Watch(ctx context.Context, sink func(sdk.Update)) error {
	t := time.NewTicker(d.o.Interval)
	defer t.Stop()
	failures := 0
	for {
		vars, err := d.poll(ctx)
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			failures++
			// The UPS turns stale if this lasts; log the first failure and
			// every tenth.
			if failures == 1 || failures%10 == 0 {
				d.log.Warn("SNMP poll failed", "host", d.o.Host, "error", err, "failures", failures)
			}
		default:
			failures = 0
			sink(sdk.Update{Vars: vars})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		case <-d.poke:
		}
	}
}

// Commands implements sdk.Device. Commands need write access.
func (d *device) Commands() []sdk.Command {
	if !d.o.canWrite() {
		return nil
	}
	names := make([]string, 0, len(d.m.commands))
	for n := range d.m.commands {
		names = append(names, n)
	}
	slices.Sort(names)
	out := make([]sdk.Command, len(names))
	for i, n := range names {
		out[i] = sdk.NewCommand(n)
	}
	return out
}

// Writable implements sdk.Device.
func (d *device) Writable() []string { return nil }

// RunCommand implements sdk.Device.
func (d *device) RunCommand(_ context.Context, name, param string) error {
	build, ok := d.m.commands[name]
	if !ok || !d.o.canWrite() {
		if ok {
			return fmt.Errorf("%w: set write_community (or use SNMPv3) to send commands", sdk.ErrNotSupported)
		}
		return sdk.ErrNotSupported
	}
	d.mu.Lock()
	last := d.last
	d.mu.Unlock()
	sets, err := build(param, last)
	if err != nil {
		return err
	}
	if err := d.set(sets); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	d.log.Info("UPS command sent", "command", name, "param", param, "sets", len(sets))
	select {
	case d.poke <- struct{}{}:
	default:
	}
	return nil
}

func (d *device) set(sets []set) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	g := d.g
	if d.o.Version != "3" {
		if d.w == nil {
			w, err := d.o.client(true)
			if err != nil {
				return err
			}
			d.w = w
		}
		g = d.w
	}
	for _, s := range sets {
		pdu := gosnmp.SnmpPDU{Name: s.oid, Type: gosnmp.Integer, Value: s.value}
		switch {
		case s.ticks:
			pdu.Type = gosnmp.TimeTicks
		case s.isOID:
			pdu.Type = gosnmp.ObjectIdentifier
		}
		r, err := g.Set([]gosnmp.SnmpPDU{pdu})
		if err != nil {
			return err
		}
		if r.Error != gosnmp.NoError {
			return fmt.Errorf("the UPS refused setting %s: %s", s.oid, r.Error)
		}
	}
	return nil
}

// SetVar implements sdk.Device.
func (d *device) SetVar(context.Context, string, string) error { return sdk.ErrNotSupported }

// Close implements sdk.Device.
func (d *device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.w != nil {
		d.w.Conn.Close()
	}
	return d.g.Conn.Close()
}
