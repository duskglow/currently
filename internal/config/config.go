// Package config loads and validates currently.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/duskglow/currently/internal/registry"
	"github.com/duskglow/currently/internal/suggest"
	"github.com/duskglow/currently/pkg/sdk"
)

// Config is the whole currently.yaml file.
type Config struct {
	UPS    map[string]UPS  `yaml:"ups"`
	Listen Listen          `yaml:"listen"`
	API    API             `yaml:"api"`
	Users  map[string]User `yaml:"users,omitempty"`
	// Shutdown is the shutdown policy. Without it, Currently only monitors and
	// never shuts this computer down.
	Shutdown *Shutdown `yaml:"shutdown,omitempty"`
	// Outputs are output plugins to run, by name.
	Outputs map[string]Output `yaml:"outputs,omitempty"`
	Plugins Plugins           `yaml:"plugins,omitempty"`
	Log     Log               `yaml:"log"`

	// Path is the file the config was loaded from; empty for defaults.
	Path string `yaml:"-"`
	// Order lists UPS names in the order they appear in the file.
	Order []string `yaml:"-"`
	// OutputOrder lists output names in file order.
	OutputOrder []string `yaml:"-"`

	lines map[string]int // dotted key path → line number
}

// UPS is one entry under `ups:`. Keys other than the ones below are passed
// to the driver as its options.
type UPS struct {
	// Driver defaults to usbhid.
	Driver      string        `yaml:"driver"`
	Description string        `yaml:"description,omitempty"`
	StaleAfter  time.Duration `yaml:"stale_after,omitempty"`
	Options     sdk.Config    `yaml:",inline"`
}

// Listen sets where Currently serves.
type Listen struct {
	// HTTP is the address of the REST API, or "off".
	HTTP string `yaml:"http"`
	// NUT is the address of the NUT protocol server (upsd compatible), or
	// "off".
	NUT string `yaml:"nut"`
	// NUTTLS enables STARTTLS on the NUT protocol server.
	NUTTLS *TLS `yaml:"nut_tls,omitempty"`
	// Metrics is empty to serve Prometheus metrics at /metrics on the HTTP
	// address, a separate host:port, or "off".
	Metrics string `yaml:"metrics,omitempty"`
	// Control is the local socket the Currently CLI talks to. Empty selects a
	// default path.
	Control string `yaml:"control,omitempty"`
	// SNMP enables the SNMP agent (UPS-MIB).
	SNMP *SNMP `yaml:"snmp,omitempty"`
}

// SNMP configures the SNMP agent. It can also be written as just an
// address (listen.snmp: ":1161").
type SNMP struct {
	// Addr is the UDP address; it defaults to DefaultSNMP.
	Addr string `yaml:"addr,omitempty"`
	// Community is the read-only SNMPv1/v2c community. It defaults to
	// "public" when there are no v3 users; with v3 users and no community,
	// v1 and v2c are off.
	Community    string `yaml:"community,omitempty"`
	CommunityEnv string `yaml:"community_env,omitempty"`
	// UPS is served when a request does not name one (with the context
	// name, or "community@ups"); it defaults to the first UPS.
	UPS string `yaml:"ups,omitempty"`
	// Users are SNMPv3 users.
	Users map[string]SNMPUser `yaml:"users,omitempty"`
	// EngineID is the SNMPv3 engine ID in hex; empty derives one from the
	// host name.
	EngineID string `yaml:"engine_id,omitempty"`
	// Traps lists SNMPv2c trap receivers.
	Traps    []SNMPTrap `yaml:"traps,omitempty"`
	Location string     `yaml:"location,omitempty"`
	Contact  string     `yaml:"contact,omitempty"`

	off bool
}

// UnmarshalYAML accepts an address or "off" in place of the mapping.
func (s *SNMP) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if strings.EqualFold(n.Value, "off") {
			s.off = true
		} else {
			s.Addr = n.Value
		}
		return nil
	}
	type plain SNMP
	return n.Decode((*plain)(s))
}

// Secret returns the effective community.
func (s *SNMP) Secret() string {
	if s.CommunityEnv != "" {
		return os.Getenv(s.CommunityEnv)
	}
	return s.Community
}

// SNMPUser is an SNMPv3 user.
type SNMPUser struct {
	// Auth is none, md5, sha, sha224, sha256, sha384 or sha512.
	Auth            string `yaml:"auth"`
	AuthPassword    string `yaml:"auth_password,omitempty"`
	AuthPasswordEnv string `yaml:"auth_password_env,omitempty"`
	// Privacy is none or aes.
	Privacy            string `yaml:"privacy,omitempty"`
	PrivacyPassword    string `yaml:"privacy_password,omitempty"`
	PrivacyPasswordEnv string `yaml:"privacy_password_env,omitempty"`
}

// Secrets returns the effective passwords.
func (u SNMPUser) Secrets() (auth, privacy string) {
	auth, privacy = u.AuthPassword, u.PrivacyPassword
	if u.AuthPasswordEnv != "" {
		auth = os.Getenv(u.AuthPasswordEnv)
	}
	if u.PrivacyPasswordEnv != "" {
		privacy = os.Getenv(u.PrivacyPasswordEnv)
	}
	return auth, privacy
}

// SNMPTrap is a trap receiver.
type SNMPTrap struct {
	// Target is host or host:port (port 162 by default).
	Target    string `yaml:"target"`
	Community string `yaml:"community,omitempty"` // default "public"
}

// TLS names a certificate and key in PEM files.
type TLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// User is an account for the NUT protocol server, as in NUT's upsd.users.
type User struct {
	Password    string `yaml:"password,omitempty"`
	PasswordEnv string `yaml:"password_env,omitempty"`
	// Role is secondary (upsmon on a machine that only watches), primary
	// (upsmon on the machine attached to the UPS; may set FSD) or admin
	// (primary, plus commands and variable changes).
	Role string `yaml:"role"`
}

// Roles of NUT users.
const (
	RoleSecondary = "secondary"
	RolePrimary   = "primary"
	RoleAdmin     = "admin"
)

var roleAliases = map[string]string{"slave": RoleSecondary, "master": RolePrimary}

// Secret returns the effective password.
func (u User) Secret() string {
	if u.PasswordEnv != "" {
		return os.Getenv(u.PasswordEnv)
	}
	return u.Password
}

// Output is one entry under `outputs:`. Keys other than the ones below are
// passed to the plugin as its options.
type Output struct {
	// Plugin names the output plugin; it defaults to the entry's name.
	Plugin string `yaml:"plugin,omitempty"`
	// AllowControl lets the plugin run commands, change variables and set
	// FSD. Without it, the plugin can only read.
	AllowControl bool       `yaml:"allow_control,omitempty"`
	Options      sdk.Config `yaml:",inline"`
}

// Plugins says where to find plugins besides the default directories.
type Plugins struct {
	// Dirs are searched for currently-driver-* and currently-output-* executables
	// before the default directories.
	Dirs []string `yaml:"dirs,omitempty"`
	// Commands start plugins that need an interpreter, such as
	// ["python3", "/opt/currently/plugins/mydriver.py"].
	Commands [][]string `yaml:"commands,omitempty"`
}

// OutputNames returns output names in file order.
func (c *Config) OutputNames() []string {
	names := slices.Clone(c.OutputOrder)
	for name := range c.Outputs {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// PluginName returns the plugin an output entry uses.
func (o Output) PluginName(entry string) string {
	if o.Plugin != "" {
		return o.Plugin
	}
	return entry
}

// Shutdown decides when to shut this computer down, and how.
type Shutdown struct {
	// UPS lists the UPSes that power this computer (default: all). UPSes
	// read with the nut driver are watched as a secondary; the others are
	// attached here, and this computer is their primary.
	UPS []string `yaml:"ups,omitempty"`
	// MinSupplies is how many of those UPSes must be healthy to keep
	// running (NUT's MINSUPPLIES), default 1.
	MinSupplies int          `yaml:"min_supplies,omitempty"`
	When        ShutdownWhen `yaml:"when"`
	// DeadTime is how long a UPS that was on battery may stay silent
	// before it counts as critical, default 15s.
	DeadTime time.Duration `yaml:"dead_time,omitempty"`
	// WaitForSecondaries is how long a primary waits for NUT clients to log
	// out after setting FSD (NUT's HOSTSYNC), default 15s.
	WaitForSecondaries time.Duration `yaml:"wait_for_secondaries,omitempty"`
	// FinalDelay is the pause before the shutdown command (NUT's
	// FINALDELAY), default 5s.
	FinalDelay time.Duration `yaml:"final_delay,omitempty"`
	// Hooks are commands run before shutting down, such as stopping VMs.
	Hooks       []string      `yaml:"hooks,omitempty"`
	HookTimeout time.Duration `yaml:"hook_timeout,omitempty"`
	// Command replaces the operating system's shutdown command.
	Command string `yaml:"command,omitempty"`
	// PowerOffUPS tells local UPSes to cut their output UPSOffDelay after
	// the shutdown starts, and to restore it when mains power returns, so
	// the computer boots again by itself.
	PowerOffUPS bool          `yaml:"power_off_ups,omitempty"`
	UPSOffDelay time.Duration `yaml:"ups_off_delay,omitempty"`
	// DryRun logs every step without doing any of them.
	DryRun bool `yaml:"dry_run,omitempty"`
}

// ShutdownWhen lists the triggers; any one makes an on-battery UPS
// critical.
type ShutdownWhen struct {
	// LowBattery follows the UPS's own low-battery flag (default true).
	LowBattery   *bool         `yaml:"low_battery,omitempty"`
	OnBatteryFor time.Duration `yaml:"on_battery_for,omitempty"`
	RuntimeBelow time.Duration `yaml:"runtime_below,omitempty"`
	ChargeBelow  float64       `yaml:"charge_below,omitempty"`
}

// Shutdown defaults.
const (
	DefaultDeadTime           = 15 * time.Second
	DefaultWaitForSecondaries = 15 * time.Second
	DefaultFinalDelay         = 5 * time.Second
	DefaultHookTimeout        = 2 * time.Minute
	DefaultUPSOffDelay        = 60 * time.Second
)

func (s *Shutdown) fillDefaults() {
	if s.MinSupplies == 0 {
		s.MinSupplies = 1
	}
	if s.When.LowBattery == nil {
		t := true
		s.When.LowBattery = &t
	}
	if s.DeadTime == 0 {
		s.DeadTime = DefaultDeadTime
	}
	if s.WaitForSecondaries == 0 {
		s.WaitForSecondaries = DefaultWaitForSecondaries
	}
	if s.FinalDelay == 0 {
		s.FinalDelay = DefaultFinalDelay
	}
	if s.HookTimeout == 0 {
		s.HookTimeout = DefaultHookTimeout
	}
	if s.UPSOffDelay == 0 {
		s.UPSOffDelay = DefaultUPSOffDelay
	}
}

// MaxSocketPath is the longest Unix socket path the platform allows.
func MaxSocketPath() int {
	if runtime.GOOS == "darwin" || runtime.GOOS == "freebsd" {
		return 103
	}
	return 107
}

// Supplies returns the UPSes that power this computer.
func (c *Config) Supplies() []string {
	if c.Shutdown != nil && len(c.Shutdown.UPS) > 0 {
		return c.Shutdown.UPS
	}
	return c.UPSNames()
}

// API sets access to the REST API over the network.
type API struct {
	// Token, or the environment variable named by TokenEnv, is the bearer
	// token required for commands and variable changes over HTTP. Without
	// one, the HTTP API is read-only.
	Token    string `yaml:"token,omitempty"`
	TokenEnv string `yaml:"token_env,omitempty"`
}

// Log sets logging.
type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Defaults.
const (
	DefaultHTTP       = "127.0.0.1:9300"
	DefaultNUT        = "127.0.0.1:3493"
	DefaultSNMP       = ":1161"
	DefaultDriver     = "usbhid"
	DefaultStaleAfter = 30 * time.Second
)

// knownKeys lists the keys allowed in each fixed section, for typo hints.
var knownKeys = map[string][]string{
	"":        {"ups", "listen", "api", "users", "shutdown", "outputs", "plugins", "log"},
	"plugins": {"dirs", "commands"},
	"shutdown": {"ups", "min_supplies", "when", "dead_time", "wait_for_secondaries", "final_delay",
		"hooks", "hook_timeout", "command", "power_off_ups", "ups_off_delay", "dry_run"},
	"shutdown.when": {"low_battery", "on_battery_for", "runtime_below", "charge_below"},
	"listen":        {"http", "nut", "nut_tls", "metrics", "control", "snmp"},
	"listen.snmp": {"addr", "community", "community_env", "ups", "users", "engine_id", "traps",
		"location", "contact"},
	"listen.nut_tls": {"cert_file", "key_file"},
	"api":            {"token", "token_env"},
	"log":            {"level", "format"},
}

var userKeys = []string{"password", "password_env", "role"}

var snmpUserKeys = []string{"auth", "auth_password", "auth_password_env", "privacy", "privacy_password",
	"privacy_password_env"}

var upsNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ValidUPSName reports whether name can be used as a UPS name.
func ValidUPSName(name string) bool { return upsNameRE.MatchString(name) }

// Default returns the configuration used when there is no config file.
func Default() *Config {
	return &Config{
		UPS:    map[string]UPS{},
		Listen: Listen{HTTP: DefaultHTTP, NUT: DefaultNUT},
		Log:    Log{Level: "info", Format: "text"},
		lines:  map[string]int{},
	}
}

// Load reads and parses a config file. It checks structure (unknown keys,
// bad values) but not drivers; call Validate for that.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(path, data)
}

// Parse parses config file contents. path is used in error messages.
func Parse(path string, data []byte) (*Config, error) {
	c := Default()
	c.Path = path
	if len(bytes.TrimSpace(data)) == 0 {
		return c, nil
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, yamlError(path, err)
	}
	if len(root.Content) == 0 {
		return c, nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, &Error{File: path, Problems: []Problem{{doc.Line, "the config file must be a mapping of settings (key: value)"}}}
	}

	var problems []Problem
	c.walk(doc, "", &problems)
	if len(problems) > 0 {
		return nil, &Error{File: path, Problems: problems}
	}
	if err := doc.Decode(c); err != nil {
		return nil, yamlError(path, err)
	}
	if c.UPS == nil {
		c.UPS = map[string]UPS{}
	}
	for _, addr := range []*string{&c.Listen.HTTP, &c.Listen.NUT} {
		if strings.EqualFold(*addr, "off") {
			*addr = ""
		}
	}
	if c.Shutdown != nil {
		c.Shutdown.fillDefaults()
	}
	if s := c.Listen.SNMP; s != nil {
		switch {
		case s.off:
			c.Listen.SNMP = nil
		case s.Addr == "":
			s.Addr = DefaultSNMP
		}
	}
	for name, u := range c.Users {
		if r, ok := roleAliases[strings.ToLower(u.Role)]; ok {
			u.Role = r
			c.Users[name] = u
		}
	}
	for name, u := range c.UPS {
		if u.Driver == "" {
			u.Driver = DefaultDriver
		}
		if u.StaleAfter == 0 {
			u.StaleAfter = DefaultStaleAfter
		}
		c.UPS[name] = u
	}
	return c, nil
}

// walk records line numbers and UPS order, and flags unknown keys in the
// fixed sections.
func (c *Config) walk(n *yaml.Node, path string, problems *[]Problem) {
	if n.Kind != yaml.MappingNode {
		return
	}
	known, checked := knownKeys[path]
	if strings.HasPrefix(path, "users.") && strings.Count(path, ".") == 1 {
		known, checked = userKeys, true
	}
	if strings.HasPrefix(path, "listen.snmp.users.") && strings.Count(path, ".") == 3 {
		known, checked = snmpUserKeys, true
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i], n.Content[i+1]
		full := key.Value
		if path != "" {
			full = path + "." + key.Value
		}
		c.lines[full] = key.Line
		if checked && !slices.Contains(known, key.Value) {
			where := "at the top level"
			if path != "" {
				where = "in " + path
			}
			*problems = append(*problems, Problem{key.Line, fmt.Sprintf("unknown setting %q %s%s; valid settings are: %s",
				key.Value, where, suggest.DidYouMean(key.Value, known), strings.Join(known, ", "))})
			continue
		}
		switch {
		case path == "ups":
			c.Order = append(c.Order, key.Value)
			c.walk(val, full, problems) // record option lines; options are checked by the driver
		case path == "outputs":
			c.OutputOrder = append(c.OutputOrder, key.Value)
			c.walk(val, full, problems)
		case path == "" || path == "listen" || path == "users" || path == "shutdown" || path == "plugins" ||
			path == "listen.snmp" || path == "listen.snmp.users" ||
			strings.HasPrefix(full, "ups.") || strings.HasPrefix(full, "outputs."):
			c.walk(val, full, problems)
		}
	}
}

// Line returns the line of a dotted key path such as "ups.rack.driver",
// or 0 when unknown.
func (c *Config) Line(path string) int { return c.lines[path] }

// Token returns the effective API token.
func (c *Config) Token() string {
	if c.API.TokenEnv != "" {
		return os.Getenv(c.API.TokenEnv)
	}
	return c.API.Token
}

// Validate checks the config against the available drivers.
func (c *Config) Validate(reg *registry.Registry) error {
	var problems []Problem
	add := func(path, format string, args ...any) {
		problems = append(problems, Problem{c.Line(path), fmt.Sprintf(format, args...)})
	}

	drivers := reg.DriverNames()
	for _, name := range c.UPSNames() {
		u := c.UPS[name]
		key := "ups." + name
		if !upsNameRE.MatchString(name) {
			add(key, "UPS name %q may only contain letters, digits, '.', '_' and '-', and must start with a letter or digit", name)
		}
		d, ok := reg.Driver(u.Driver)
		if !ok {
			add(key+".driver", "UPS %s: unknown driver %q%s; available drivers: %s",
				name, u.Driver, suggest.DidYouMean(u.Driver, drivers), strings.Join(drivers, ", "))
			continue
		}
		if u.StaleAfter < 0 {
			add(key+".stale_after", "UPS %s: stale_after must not be negative", name)
		}
		if err := d.ValidateConfig(u.Options); err != nil {
			for _, msg := range strings.Split(err.Error(), "\n") {
				add(c.optionPath(key, msg), "UPS %s (%s driver): %s", name, u.Driver, msg)
			}
		}
	}

	checkAddr := func(key, addr, example string) {
		if _, port, err := net.SplitHostPort(addr); err != nil {
			add(key, "%s %q is not a host:port address (for example %q, or %q for all interfaces), or \"off\"",
				key, addr, example, example[strings.LastIndex(example, ":"):])
		} else if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			add(key, "%s port %q is not a number between 0 and 65535", key, port)
		}
	}
	if c.Listen.HTTP != "" {
		checkAddr("listen.http", c.Listen.HTTP, DefaultHTTP)
	}
	if c.Listen.NUT != "" {
		checkAddr("listen.nut", c.Listen.NUT, DefaultNUT)
	}
	if n, limit := len(c.Listen.Control), MaxSocketPath(); n > limit {
		add("listen.control", "listen.control is %d characters long, but socket paths are limited to %d; choose a shorter path", n, limit)
	}
	switch m := strings.ToLower(c.Listen.Metrics); m {
	case "", "off":
		if m == "" && c.Listen.HTTP == "" {
			add("listen.metrics", "metrics are served on listen.http, which is off; set listen.metrics to a host:port or \"off\"")
		}
	default:
		checkAddr("listen.metrics", c.Listen.Metrics, "127.0.0.1:9301")
	}
	if t := c.Listen.NUTTLS; t != nil {
		for key, f := range map[string]string{"cert_file": t.CertFile, "key_file": t.KeyFile} {
			if f == "" {
				add("listen.nut_tls", "listen.nut_tls.%s is required", key)
			} else if _, err := os.Stat(f); err != nil {
				add("listen.nut_tls."+key, "listen.nut_tls.%s: %v", key, err)
			}
		}
	}
	if s := c.Listen.SNMP; s != nil {
		c.validateSNMP(s, checkAddr, add)
	}
	roles := []string{RoleSecondary, RolePrimary, RoleAdmin}
	for _, name := range slices.Sorted(maps.Keys(c.Users)) {
		u := c.Users[name]
		key := "users." + name
		if strings.ContainsAny(name, " 	\"") {
			add(key, "user name %q must not contain spaces or quotes", name)
		}
		if !slices.Contains(roles, u.Role) {
			add(key+".role", "user %s: role %q is not one of %s%s", name, u.Role, strings.Join(roles, ", "), suggest.DidYouMean(u.Role, roles))
		}
		switch {
		case u.Password != "" && u.PasswordEnv != "":
			add(key+".password", "user %s: set either password or password_env, not both", name)
		case u.Password == "" && u.PasswordEnv == "":
			add(key, "user %s: set password or password_env", name)
		case u.PasswordEnv != "" && os.Getenv(u.PasswordEnv) == "":
			add(key+".password_env", "user %s: password_env names environment variable %s, but it is not set", name, u.PasswordEnv)
		}
	}
	if c.API.Token != "" && c.API.TokenEnv != "" {
		add("api.token", "set either api.token or api.token_env, not both")
	}
	if c.API.TokenEnv != "" && os.Getenv(c.API.TokenEnv) == "" {
		add("api.token_env", "api.token_env names environment variable %s, but it is not set", c.API.TokenEnv)
	}
	if sd := c.Shutdown; sd != nil {
		names := c.UPSNames()
		for _, u := range sd.UPS {
			if _, ok := c.UPS[u]; !ok {
				add("shutdown.ups", "shutdown.ups: no UPS named %q%s (configured: %s)", u, suggest.DidYouMean(u, names), strings.Join(names, ", "))
			}
		}
		if len(c.Supplies()) == 0 {
			add("shutdown", "shutdown is set but no UPS is configured")
		}
		if sd.MinSupplies < 1 || sd.MinSupplies > max(1, len(c.Supplies())) {
			add("shutdown.min_supplies", "shutdown.min_supplies must be between 1 and the number of UPSes powering this computer (%d)", len(c.Supplies()))
		}
		w := sd.When
		if !*w.LowBattery && w.OnBatteryFor == 0 && w.RuntimeBelow == 0 && w.ChargeBelow == 0 {
			add("shutdown.when", "shutdown.when: nothing would trigger a shutdown; keep low_battery: true or set on_battery_for, runtime_below or charge_below")
		}
		if w.ChargeBelow < 0 || w.ChargeBelow >= 100 {
			add("shutdown.when.charge_below", "shutdown.when.charge_below must be between 0 and 100")
		}
		for key, d := range map[string]time.Duration{"when.on_battery_for": w.OnBatteryFor, "when.runtime_below": w.RuntimeBelow,
			"dead_time": sd.DeadTime, "wait_for_secondaries": sd.WaitForSecondaries, "final_delay": sd.FinalDelay,
			"hook_timeout": sd.HookTimeout, "ups_off_delay": sd.UPSOffDelay} {
			if d < 0 {
				add("shutdown."+key, "shutdown.%s must not be negative", key)
			}
		}
		if sd.PowerOffUPS && sd.UPSOffDelay < 10*time.Second {
			add("shutdown.ups_off_delay", "shutdown.ups_off_delay must be at least 10s, so this computer finishes shutting down before the UPS cuts power")
		}
		for i, h := range sd.Hooks {
			if strings.TrimSpace(h) == "" {
				add("shutdown.hooks", "shutdown.hooks[%d] is empty", i)
			}
		}
	}

	outputs := reg.OutputNames()
	for _, name := range c.OutputNames() {
		o := c.Outputs[name]
		key := "outputs." + name
		plugin := o.PluginName(name)
		out, ok := reg.Output(plugin)
		if !ok {
			hint := "no output plugins are installed"
			if len(outputs) > 0 {
				hint = "installed: " + strings.Join(outputs, ", ")
			}
			add(key, "output %s: no output plugin named %q%s (%s; see `currently plugin list`)", name, plugin, suggest.DidYouMean(plugin, outputs), hint)
			continue
		}
		if err := out.ValidateConfig(o.Options); err != nil {
			for _, msg := range strings.Split(err.Error(), "\n") {
				add(c.optionPath(key, msg), "output %s (%s plugin): %s", name, plugin, msg)
			}
		}
	}
	for i, cmd := range c.Plugins.Commands {
		if len(cmd) == 0 || strings.TrimSpace(cmd[0]) == "" {
			add("plugins.commands", "plugins.commands[%d] is empty", i)
		}
	}

	levels := []string{"debug", "info", "warn", "error"}
	if !slices.Contains(levels, c.Log.Level) {
		add("log.level", "log.level %q is not one of %s%s", c.Log.Level, strings.Join(levels, ", "), suggest.DidYouMean(c.Log.Level, levels))
	}
	formats := []string{"text", "json"}
	if !slices.Contains(formats, c.Log.Format) {
		add("log.format", "log.format %q is not one of %s", c.Log.Format, strings.Join(formats, ", "))
	}

	if len(problems) > 0 {
		return &Error{File: c.Path, Problems: problems}
	}
	return nil
}

// optionPath guesses which option a driver error is about, so the error
// can point at its line: drivers name the option first ("load must be ...").
func (c *Config) optionPath(upsKey, msg string) string {
	word, _, _ := strings.Cut(msg, " ")
	if strings.HasPrefix(word, "unknown") {
		if _, rest, ok := strings.Cut(msg, `"`); ok {
			word, _, _ = strings.Cut(rest, `"`)
		}
	}
	if p := upsKey + "." + word; c.Line(p) > 0 {
		return p
	}
	return upsKey
}

// UPSNames returns UPS names in file order.
func (c *Config) UPSNames() []string {
	names := slices.Clone(c.Order)
	for name := range c.UPS { // UPSes added in code, such as the demo
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// AddUPS adds a UPS defined in code (not in the file), filling in defaults.
func (c *Config) AddUPS(name string, u UPS) {
	if _, ok := c.UPS[name]; ok {
		return
	}
	if u.Driver == "" {
		u.Driver = DefaultDriver
	}
	if u.StaleAfter == 0 {
		u.StaleAfter = DefaultStaleAfter
	}
	c.UPS[name] = u
	c.Order = append(c.Order, name)
}

// AddDemo adds a simulated UPS named "demo" whose mains power fails for a
// minute every three minutes.
func (c *Config) AddDemo() {
	c.AddUPS("demo", UPS{
		Driver:      "simulator",
		Description: "Simulated UPS (--demo)",
		Options:     sdk.Config{"scenario": "cycle", "online_for": "2m", "outage_for": "1m"},
	})
}

// YAML renders the effective config, with secrets hidden.
func (c *Config) YAML() ([]byte, error) {
	out := *c
	if out.API.Token != "" {
		out.API.Token = "<hidden>"
	}
	if out.Listen.HTTP == "" {
		out.Listen.HTTP = "off"
	}
	if out.Listen.NUT == "" {
		out.Listen.NUT = "off"
	}
	if s := c.Listen.SNMP; s != nil {
		cp := *s
		if cp.Community != "" {
			cp.Community = "<hidden>"
		}
		if len(s.Users) > 0 {
			cp.Users = map[string]SNMPUser{}
			for k, u := range s.Users {
				if u.AuthPassword != "" {
					u.AuthPassword = "<hidden>"
				}
				if u.PrivacyPassword != "" {
					u.PrivacyPassword = "<hidden>"
				}
				cp.Users[k] = u
			}
		}
		out.Listen.SNMP = &cp
	}
	if len(out.Users) > 0 {
		out.Users = map[string]User{}
		for k, u := range c.Users {
			if u.Password != "" {
				u.Password = "<hidden>"
			}
			out.Users[k] = u
		}
	}
	return yaml.Marshal(&out)
}

// Problem is one error at a line of the config file.
type Problem struct {
	Line int
	Msg  string
}

// Error lists the problems found in a config file.
type Error struct {
	File     string
	Problems []Problem
}

func (e *Error) Error() string {
	var b strings.Builder
	for i, p := range e.Problems {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch {
		case e.File != "" && p.Line > 0:
			fmt.Fprintf(&b, "%s:%d: %s", e.File, p.Line, p.Msg)
		case e.File != "":
			fmt.Fprintf(&b, "%s: %s", e.File, p.Msg)
		default:
			b.WriteString(p.Msg)
		}
	}
	return b.String()
}

var yamlLineRE = regexp.MustCompile(`^line (\d+): (.*)$`)

// yamlError turns a yaml.v3 error into an *Error with line numbers.
func yamlError(path string, err error) error {
	var lines []string
	var te *yaml.TypeError
	if errors.As(err, &te) {
		lines = te.Errors
	} else {
		lines = []string{strings.TrimPrefix(err.Error(), "yaml: ")}
	}
	e := &Error{File: path}
	for _, l := range lines {
		p := Problem{Msg: l}
		if m := yamlLineRE.FindStringSubmatch(l); m != nil {
			p.Line, _ = strconv.Atoi(m[1])
			p.Msg = m[2]
		}
		e.Problems = append(e.Problems, p)
	}
	return e
}
