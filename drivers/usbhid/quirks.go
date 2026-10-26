package usbhid

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nut-ng/nut-ng/pkg/sdk"
)

//go:embed quirks/*.yaml
var builtinQuirks embed.FS

// QuirkFile maps a family of devices' fields to NUT variables. See
// quirks/00-generic.yaml for the format.
type QuirkFile struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Verified lists models the file was checked against.
	Verified []string `yaml:"verified"`
	Match    struct {
		VendorIDs []string `yaml:"vendor_ids"`
	} `yaml:"match"`
	Vars     map[string]VarRule     `yaml:"vars"`
	Flags    []FlagRule             `yaml:"flags"`
	Commands map[string]CommandRule `yaml:"commands"`
	Fixups   []string               `yaml:"fixups"`

	Source string `yaml:"-"` // file it was loaded from
}

// VarRule produces one variable.
type VarRule struct {
	Paths []string `yaml:"paths"`
	// Decimals fixes the number of decimals; by default whole numbers have
	// none and others one.
	Decimals *int `yaml:"decimals"`
	// Scale multiplies the physical value.
	Scale float64 `yaml:"scale"`
	// Enum maps raw values to text.
	Enum map[int64]string `yaml:"enum"`
	// String means the value is the index of a USB string descriptor.
	String bool `yaml:"string"`
	// Convert: "celsius" (temperatures, converted from kelvin when needed)
	// or "hid_date" (HID battery date encoding).
	Convert  string `yaml:"convert"`
	Writable bool   `yaml:"writable"`
	// NegativeIsNone reports any negative value as -1 ("none"), for
	// countdowns whose descriptors scale the -1 sentinel (CyberPower
	// reports -60).
	NegativeIsNone bool `yaml:"negative_is_none"`
}

// FlagRule sets a ups.status flag when a field equals When (default 1).
type FlagRule struct {
	Flag  string   `yaml:"flag"`
	Paths []string `yaml:"paths"`
	When  *int64   `yaml:"when"`
}

// CommandRule writes Value to the first available path, or performs the
// Writes in order.
type CommandRule struct {
	Paths       []string       `yaml:"paths"`
	Value       int64          `yaml:"value"`
	Writes      []CommandWrite `yaml:"writes"`
	Description string         `yaml:"description"`
}

// CommandWrite is one field write of a command. Exactly one of Value (a
// raw value), Physical (in the field's units, such as seconds) or Param
// (the command parameter, in the field's units) gives the value.
type CommandWrite struct {
	Paths    []string `yaml:"paths"`
	Value    *int64   `yaml:"value"`
	Physical *float64 `yaml:"physical"`
	Param    bool     `yaml:"param"`
	// Default is used when Param is set and no parameter is given.
	Default float64 `yaml:"default"`
}

// writes returns the rule as a list of writes.
func (r CommandRule) writes() []CommandWrite {
	if len(r.Writes) > 0 {
		return r.Writes
	}
	v := r.Value
	return []CommandWrite{{Paths: r.Paths, Value: &v}}
}

var vendorIDRE = regexp.MustCompile(`^[0-9a-fA-F]{4}$`)

// ParseQuirkFile parses and checks one quirk file.
func ParseQuirkFile(name string, data []byte) (*QuirkFile, error) {
	var q QuirkFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&q); err != nil {
		return nil, fmt.Errorf("%s: %s", name, strings.TrimPrefix(err.Error(), "yaml: "))
	}
	q.Source = name
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(name+": "+format, args...)) }
	if q.Name == "" {
		bad("name is required")
	}
	for _, id := range q.Match.VendorIDs {
		if !vendorIDRE.MatchString(id) {
			bad("vendor ID %q must be 4 hex digits, quoted", id)
		}
	}
	for v, r := range q.Vars {
		if len(r.Paths) == 0 {
			bad("variable %s has no paths", v)
		}
		if r.Convert != "" && r.Convert != "celsius" && r.Convert != "hid_date" {
			bad("variable %s: unknown convert %q (celsius, hid_date)", v, r.Convert)
		}
	}
	for _, f := range q.Flags {
		if _, ok := statusFlags[sdk.Flag(f.Flag)]; !ok {
			bad("unknown status flag %q", f.Flag)
		}
		if len(f.Paths) == 0 {
			bad("flag %s has no paths", f.Flag)
		}
	}
	for c, r := range q.Commands {
		if len(r.Writes) > 0 && len(r.Paths) > 0 {
			bad("command %s: use either paths/value or writes", c)
		}
		for i, w := range r.writes() {
			n := 0
			for _, set := range []bool{w.Value != nil, w.Physical != nil, w.Param} {
				if set {
					n++
				}
			}
			if len(w.Paths) == 0 {
				bad("command %s: write %d has no paths", c, i+1)
			}
			if n != 1 {
				bad("command %s: write %d needs exactly one of value, physical or param", c, i+1)
			}
		}
	}
	for _, f := range q.Fixups {
		if _, ok := fixups[f]; !ok {
			bad("unknown fixup %q", f)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &q, nil
}

var statusFlags = map[sdk.Flag]bool{
	sdk.FlagOnline: true, sdk.FlagOnBattery: true, sdk.FlagLowBattery: true, sdk.FlagHighBattery: true,
	sdk.FlagReplaceBattery: true, sdk.FlagCharging: true, sdk.FlagDischarging: true, sdk.FlagBypass: true,
	sdk.FlagCalibrating: true, sdk.FlagOff: true, sdk.FlagOverload: true, sdk.FlagTrim: true,
	sdk.FlagBoost: true, sdk.FlagForcedShutdown: true, sdk.FlagAlarm: true, sdk.FlagTest: true,
}

// LoadQuirks returns the built-in quirk files followed by *.yaml files in
// dir (which may be empty or missing).
func LoadQuirks(dir string) ([]*QuirkFile, error) {
	var out []*QuirkFile
	entries, err := builtinQuirks.ReadDir("quirks")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		data, err := builtinQuirks.ReadFile("quirks/" + e.Name())
		if err != nil {
			return nil, err
		}
		q, err := ParseQuirkFile("builtin:"+e.Name(), data)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	if dir == "" {
		return out, nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	sort.Strings(files)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		q, err := ParseQuirkFile(f, data)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, nil
}

// SelectQuirks returns the files that apply to a vendor ID: files without a
// match section, and files listing the vendor, in load order.
func SelectQuirks(all []*QuirkFile, vendorID uint16) []*QuirkFile {
	vid := fmt.Sprintf("%04x", vendorID)
	var out []*QuirkFile
	for _, q := range all {
		if len(q.Match.VendorIDs) == 0 || slices.ContainsFunc(q.Match.VendorIDs, func(s string) bool { return strings.EqualFold(s, vid) }) {
			out = append(out, q)
		}
	}
	return out
}
