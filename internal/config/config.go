// Package config loads and validates nutng.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nut-ng/nut-ng/internal/registry"
	"github.com/nut-ng/nut-ng/internal/suggest"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// Config is the whole nutng.yaml file.
type Config struct {
	UPS    map[string]UPS `yaml:"ups"`
	Listen Listen         `yaml:"listen"`
	API    API            `yaml:"api"`
	Log    Log            `yaml:"log"`

	// Path is the file the config was loaded from; empty for defaults.
	Path string `yaml:"-"`
	// Order lists UPS names in the order they appear in the file.
	Order []string `yaml:"-"`

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

// Listen sets where nutng serves.
type Listen struct {
	// HTTP is the address of the REST API, or "off".
	HTTP string `yaml:"http"`
	// Control is the local socket the nutng CLI talks to. Empty selects a
	// default path.
	Control string `yaml:"control,omitempty"`
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
	DefaultDriver     = "usbhid"
	DefaultStaleAfter = 30 * time.Second
)

// knownKeys lists the keys allowed in each fixed section, for typo hints.
var knownKeys = map[string][]string{
	"":       {"ups", "listen", "api", "log"},
	"listen": {"http", "control"},
	"api":    {"token", "token_env"},
	"log":    {"level", "format"},
}

var upsNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ValidUPSName reports whether name can be used as a UPS name.
func ValidUPSName(name string) bool { return upsNameRE.MatchString(name) }

// Default returns the configuration used when there is no config file.
func Default() *Config {
	return &Config{
		UPS:    map[string]UPS{},
		Listen: Listen{HTTP: DefaultHTTP},
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
	if strings.EqualFold(c.Listen.HTTP, "off") {
		c.Listen.HTTP = ""
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
		case path == "" || strings.HasPrefix(full, "ups."):
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

	if c.Listen.HTTP != "" {
		if _, port, err := net.SplitHostPort(c.Listen.HTTP); err != nil {
			add("listen.http", "listen.http %q is not a host:port address (for example %q, or \":9300\" for all interfaces), or \"off\"", c.Listen.HTTP, DefaultHTTP)
		} else if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			add("listen.http", "listen.http port %q is not a number between 0 and 65535", port)
		}
	}
	if c.API.Token != "" && c.API.TokenEnv != "" {
		add("api.token", "set either api.token or api.token_env, not both")
	}
	if c.API.TokenEnv != "" && os.Getenv(c.API.TokenEnv) == "" {
		add("api.token_env", "api.token_env names environment variable %s, but it is not set", c.API.TokenEnv)
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
