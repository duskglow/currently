// Package execdriver is the "exec" driver: any program that prints UPS
// variables becomes a UPS. It suits quick integrations in any language and
// wraps existing tools; `upsc ups@host` works as is.
//
// The program prints either JSON objects, one per line:
//
//	{"ups.status": "OL", "battery.charge": 87}
//
// or "name: value" lines, as upsc does:
//
//	ups.status: OL
//	battery.charge: 87
//
// In poll mode (the default) the program runs every interval and each run
// is one reading. In stream mode it keeps running, and each JSON line (or
// block of name: value lines ending with a blank line) is one reading.
package execdriver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/duskglow/currently/pkg/sdk"
)

const (
	// DriverName is the name used in `driver:` in currently.yaml.
	DriverName = "exec"
	// Version of the driver.
	Version = "0.1.0"
)

// Command is a program and its arguments. In YAML it is a list, or a
// string run by the shell.
type Command []string

// UnmarshalYAML accepts a string or a list of strings.
func (c *Command) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*c = shell(n.Value)
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := n.Decode(&list); err != nil {
			return err
		}
		*c = list
		return nil
	}
	return errors.New("command must be a string or a list of strings")
}

func shell(s string) Command {
	if runtime.GOOS == "windows" {
		return Command{"cmd.exe", "/C", s}
	}
	return Command{"/bin/sh", "-c", s}
}

// Options configure the driver.
type Options struct {
	Command  Command       `yaml:"command"`
	Mode     string        `yaml:"mode"`
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
	// Commands map instant command names to programs. The parameter, if
	// any, is in the CURRENTLY_PARAM environment variable.
	Commands map[string]Command `yaml:"commands"`
}

func parseOptions(cfg sdk.Config) (Options, error) {
	o := Options{Mode: "poll", Interval: 5 * time.Second, Timeout: 10 * time.Second}
	if err := cfg.Decode(&o); err != nil {
		return o, err
	}
	var errs []error
	if len(o.Command) == 0 || strings.TrimSpace(o.Command[0]) == "" {
		errs = append(errs, errors.New(`command is required: the program that prints the UPS's variables, such as "upsc myups@nas"`))
	}
	if o.Mode != "poll" && o.Mode != "stream" {
		errs = append(errs, fmt.Errorf("mode %q is not poll or stream", o.Mode))
	}
	if o.Interval < 500*time.Millisecond {
		errs = append(errs, errors.New("interval must be at least 500ms"))
	}
	if o.Timeout <= 0 {
		errs = append(errs, errors.New("timeout must be greater than 0"))
	}
	for name, c := range o.Commands {
		if len(c) == 0 {
			errs = append(errs, fmt.Errorf("commands.%s is empty", name))
		}
	}
	return o, errors.Join(errs...)
}

// Driver is the exec driver.
type Driver struct{}

// New returns the exec driver.
func New() *Driver { return &Driver{} }

// Info implements sdk.Driver.
func (*Driver) Info() sdk.Info {
	return sdk.Info{Name: DriverName, Kind: sdk.KindDriver, Version: Version,
		Description: "Any program that prints UPS variables (JSON or name: value lines)"}
}

// Discover implements sdk.Driver.
func (*Driver) Discover(context.Context) ([]sdk.Discovered, error) { return nil, nil }

// ValidateConfig implements sdk.Driver.
func (*Driver) ValidateConfig(cfg sdk.Config) error {
	_, err := parseOptions(cfg)
	return err
}

// Open implements sdk.Driver.
func (*Driver) Open(_ context.Context, cfg sdk.Config, log *slog.Logger) (sdk.Device, error) {
	o, err := parseOptions(cfg)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath(o.Command[0]); err != nil {
		return nil, fmt.Errorf("command %s: %w", o.Command[0], err)
	}
	return &device{o: o, log: log}, nil
}

type device struct {
	o   Options
	log *slog.Logger
}

func (d *device) Watch(ctx context.Context, sink func(sdk.Update)) error {
	if d.o.Mode == "stream" {
		return d.stream(ctx, sink)
	}
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
			// Keep trying: the UPS turns stale if this lasts; report the
			// first failure and every tenth.
			if failures == 1 || failures%10 == 0 {
				d.log.Warn("exec driver command failed", "error", err, "failures", failures)
			}
		default:
			failures = 0
			sink(sdk.Update{Vars: vars})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (d *device) poll(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, d.o.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.o.Command[0], d.o.Command[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	vars := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if err := parseLine(line, vars); err != nil {
			return nil, err
		}
	}
	if len(vars) == 0 {
		return nil, errors.New("the command printed no variables")
	}
	return vars, nil
}

func (d *device) stream(ctx context.Context, sink func(sdk.Update)) error {
	cmd := exec.CommandContext(ctx, d.o.Command[0], d.o.Command[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	block := map[string]string{}
	flush := func() {
		if len(block) > 0 {
			sink(sdk.Update{Vars: block})
			block = map[string]string{}
		}
	}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "{"):
			flush()
			if err := parseLine(line, block); err != nil {
				d.log.Warn("exec driver: bad line", "error", err)
				block = map[string]string{}
				continue
			}
			flush()
		default:
			if err := parseLine(line, block); err != nil {
				d.log.Warn("exec driver: bad line", "error", err)
			}
		}
	}
	flush()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	msg := strings.TrimSpace(stderr.String())
	if err == nil {
		err = errors.New("command exited")
	}
	if msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// parseLine adds one line of output to vars.
func parseLine(line string, vars map[string]string) error {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil
	}
	if strings.HasPrefix(line, "{") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return fmt.Errorf("not a JSON object: %w", err)
		}
		for k, v := range m {
			switch x := v.(type) {
			case string:
				vars[k] = x
			case float64:
				vars[k] = strconv.FormatFloat(x, 'f', -1, 64)
			case bool:
				vars[k] = strconv.FormatBool(x)
			case nil:
			default:
				return fmt.Errorf("value of %s must be a string, number or boolean", k)
			}
		}
		return nil
	}
	k, v, ok := strings.Cut(line, ":")
	if !ok || strings.ContainsAny(strings.TrimSpace(k), " \t") {
		return fmt.Errorf("expected \"name: value\" or a JSON object, got %q", line)
	}
	vars[strings.TrimSpace(k)] = strings.TrimSpace(v)
	return nil
}

func (d *device) Commands() []sdk.Command {
	names := make([]string, 0, len(d.o.Commands))
	for n := range d.o.Commands {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]sdk.Command, len(names))
	for i, n := range names {
		out[i] = sdk.NewCommand(n)
	}
	return out
}

func (d *device) Writable() []string { return nil }

func (d *device) RunCommand(ctx context.Context, name, param string) error {
	c, ok := d.o.Commands[name]
	if !ok {
		return fmt.Errorf("command %q: %w", name, sdk.ErrNotSupported)
	}
	ctx, cancel := context.WithTimeout(ctx, d.o.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c[0], c[1:]...)
	cmd.Env = append(os.Environ(), "CURRENTLY_PARAM="+param)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d *device) SetVar(_ context.Context, name, _ string) error {
	return fmt.Errorf("variable %q: %w", name, sdk.ErrNotSupported)
}

func (d *device) Close() error { return nil }
