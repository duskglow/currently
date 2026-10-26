package usbhid

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/duskglow/currently/drivers/usbhid/hid"
	"github.com/duskglow/currently/pkg/sdk"
)

// Mapper turns raw field values into NUT variables, using quirk files.
type Mapper struct {
	info     DeviceInfo
	vars     []varBinding
	flags    []flagBinding
	commands map[string]cmdBinding
	writable map[string]varBinding
	fixups   []string
	quirks   []string

	strings map[int64]string
	noStr   map[int64]bool
}

type varBinding struct {
	name  string
	rule  VarRule
	field *hid.Field
}

type flagBinding struct {
	flag  sdk.Flag
	field *hid.Field
	when  int64
}

type cmdBinding struct {
	cmd    sdk.Command
	writes []writeBinding
}

type writeBinding struct {
	field *hid.Field
	rule  CommandWrite
}

// Write is one field write produced by a command.
type Write struct {
	Path  string
	Value int64
}

// NewMapper binds quirk rules to the fields the device has. Later quirk
// files replace earlier variables and commands of the same name.
func NewMapper(info DeviceInfo, fields []hid.Field, quirks []*QuirkFile) *Mapper {
	m := &Mapper{info: info, commands: map[string]cmdBinding{}, writable: map[string]varBinding{},
		strings: map[int64]string{}, noStr: map[int64]bool{}}
	idx := fieldIndex(fields)
	first := func(paths []string) *hid.Field {
		for _, p := range paths {
			if f, ok := idx[p]; ok {
				return f
			}
		}
		return nil
	}

	rules := map[string]VarRule{}
	cmds := map[string]CommandRule{}
	var flags []FlagRule
	fixupSet := map[string]bool{}
	for _, q := range quirks {
		m.quirks = append(m.quirks, q.Name)
		for k, v := range q.Vars {
			rules[k] = v
		}
		for k, v := range q.Commands {
			cmds[k] = v
		}
		flags = append(flags, q.Flags...)
		for _, f := range q.Fixups {
			if !fixupSet[f] {
				fixupSet[f] = true
				m.fixups = append(m.fixups, f)
			}
		}
	}

	for name, r := range rules {
		f := first(r.Paths)
		if f == nil {
			continue
		}
		b := varBinding{name: name, rule: r, field: f}
		m.vars = append(m.vars, b)
		if r.Writable && f.Type == hid.Feature && !r.String && r.Enum == nil && r.Convert == "" {
			m.writable[name] = b
		}
	}
	sort.Slice(m.vars, func(i, j int) bool { return m.vars[i].name < m.vars[j].name })

	for _, r := range flags {
		if f := first(r.Paths); f != nil {
			when := int64(1)
			if r.When != nil {
				when = *r.When
			}
			m.flags = append(m.flags, flagBinding{flag: sdk.Flag(r.Flag), field: f, when: when})
		}
	}

	for name, r := range cmds {
		b := cmdBinding{cmd: sdk.NewCommand(name)}
		if r.Description != "" {
			b.cmd.Description = r.Description
		}
		ok := true
		for _, w := range r.writes() {
			f := first(w.Paths)
			if f == nil || f.Type != hid.Feature {
				ok = false // every write must be possible
				break
			}
			b.writes = append(b.writes, writeBinding{field: f, rule: w})
		}
		if ok {
			m.commands[name] = b
		}
	}
	return m
}

// Quirks returns the names of the quirk files in use.
func (m *Mapper) Quirks() []string { return m.quirks }

// Unmapped returns the paths of fields no variable, flag or command uses,
// sorted: candidates for a quirk file.
func (m *Mapper) Unmapped(fields []hid.Field) []string {
	used := map[string]bool{}
	for _, b := range m.vars {
		used[b.field.Path] = true
	}
	for _, f := range m.flags {
		used[f.field.Path] = true
	}
	for _, c := range m.commands {
		for _, w := range c.writes {
			used[w.field.Path] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		if !used[f.Path] && !seen[f.Path] {
			seen[f.Path] = true
			out = append(out, f.Path)
		}
	}
	sort.Strings(out)
	return out
}

// Vars converts raw values to variables. str reads USB strings by index.
func (m *Mapper) Vars(values map[string]int64, str func(int) (string, error)) map[string]string {
	out := map[string]string{
		"device.type":    "ups",
		"driver.name":    DriverName,
		"driver.version": Version,
		"ups.vendorid":   fmt.Sprintf("%04x", m.info.VendorID),
		"ups.productid":  fmt.Sprintf("%04x", m.info.ProductID),
	}
	setIf := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			out[k] = v
		}
	}
	setIf("device.mfr", m.info.Manufacturer)
	setIf("ups.mfr", m.info.Manufacturer)
	setIf("device.model", m.info.Product)
	setIf("ups.model", m.info.Product)
	setIf("device.serial", m.info.Serial)
	setIf("ups.serial", m.info.Serial)

	for _, b := range m.vars {
		raw, ok := values[b.field.Path]
		if !ok {
			continue
		}
		if s, ok := m.value(b, raw, str); ok {
			out[b.name] = s
		}
	}
	for _, name := range m.fixups {
		fixups[name](out)
	}
	if st := m.status(values); len(st) > 0 {
		out["ups.status"] = st.String()
	}
	return out
}

func (m *Mapper) value(b varBinding, raw int64, str func(int) (string, error)) (string, bool) {
	r := b.rule
	switch {
	case r.String:
		if raw <= 0 || str == nil || m.noStr[raw] {
			return "", false
		}
		if s, ok := m.strings[raw]; ok {
			return s, s != ""
		}
		s, err := str(int(raw))
		if err != nil {
			m.noStr[raw] = true
			return "", false
		}
		s = strings.TrimSpace(s)
		m.strings[raw] = s
		return s, s != ""
	case r.Enum != nil:
		if s, ok := r.Enum[raw]; ok {
			return s, true
		}
		return strconv.FormatInt(raw, 10), true
	case r.Convert == "hid_date":
		// (year - 1980) * 512 + month * 32 + day
		y, mo, d := raw>>9+1980, (raw>>5)&0xF, raw&0x1F
		if mo < 1 || mo > 12 || d < 1 {
			return "", false
		}
		return fmt.Sprintf("%04d-%02d-%02d", y, mo, d), true
	}

	if r.NegativeIsNone && raw < 0 {
		return "-1", true
	}
	v := b.field.Physical(raw)
	if r.Convert == "celsius" && (b.field.Unit == hid.UnitKelvin || v > 200) {
		v -= 273.15
	}
	if r.Scale != 0 {
		v *= r.Scale
	}
	return formatNumber(v, r.Decimals), true
}

func formatNumber(v float64, decimals *int) string {
	if decimals != nil {
		return strconv.FormatFloat(v, 'f', *decimals, 64)
	}
	r := math.Round(v*10) / 10
	if r == math.Trunc(r) {
		return strconv.FormatFloat(r, 'f', 0, 64)
	}
	return strconv.FormatFloat(r, 'f', 1, 64)
}

// status builds ups.status from the flag rules: OL or OB first, then the
// other flags in rule order.
func (m *Mapper) status(values map[string]int64) sdk.Status {
	seen := map[sdk.Flag]bool{}
	var power, rest sdk.Status
	for _, fb := range m.flags {
		v, ok := values[fb.field.Path]
		if !ok || v != fb.when || seen[fb.flag] {
			continue
		}
		seen[fb.flag] = true
		if fb.flag == sdk.FlagOnline || fb.flag == sdk.FlagOnBattery {
			power = append(power, fb.flag)
		} else {
			rest = append(rest, fb.flag)
		}
	}
	if len(power) == 0 && seen[sdk.FlagDischarging] {
		power = sdk.Status{sdk.FlagOnBattery}
	}
	return append(power, rest...)
}

// Commands lists the commands the device supports, sorted.
func (m *Mapper) Commands() []sdk.Command {
	out := make([]sdk.Command, 0, len(m.commands))
	for _, c := range m.commands {
		out = append(out, c.cmd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Writable lists the writable variables, sorted.
func (m *Mapper) Writable() []string {
	out := make([]string, 0, len(m.writable))
	for k := range m.writable {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Command returns the field writes for an instant command. param is the
// command's parameter, such as a delay in seconds.
func (m *Mapper) Command(name, param string) ([]Write, error) {
	c, ok := m.commands[name]
	if !ok {
		return nil, fmt.Errorf("command %q: %w", name, sdk.ErrNotSupported)
	}
	var out []Write
	for _, w := range c.writes {
		f, r := w.field, w.rule
		switch {
		case r.Value != nil:
			out = append(out, Write{f.Path, *r.Value})
			continue
		case r.Physical != nil:
			raw, err := exactRaw(f, *r.Physical)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			out = append(out, Write{f.Path, raw})
			continue
		}
		v := r.Default
		if strings.TrimSpace(param) != "" {
			p, err := strconv.ParseFloat(strings.TrimSpace(param), 64)
			if err != nil {
				return nil, fmt.Errorf("%s: parameter must be a number, got %q: %w", name, param, sdk.ErrInvalid)
			}
			v = p
		}
		raw, err := exactRaw(f, v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, Write{f.Path, raw})
	}
	return out, nil
}

// exactRaw converts a physical value to raw, refusing values the device
// cannot represent within 10% (for example a 20 s delay on a UPS that
// counts delays in minutes, which would round to 0: power off now).
func exactRaw(f *hid.Field, v float64) (int64, error) {
	raw := f.Raw(v)
	got := f.Physical(raw)
	if math.Abs(got-v) > math.Max(0.5, math.Abs(v)*0.1) {
		step := f.Physical(1) - f.Physical(0)
		return 0, fmt.Errorf("%s cannot be set to %s; the device works in steps of %s (nearest %s): %w",
			f.Path, formatNumber(v, nil), formatNumber(step, nil), formatNumber(got, nil), sdk.ErrInvalid)
	}
	return raw, nil
}

// SetVar returns the field write for setting a variable.
func (m *Mapper) SetVar(name, value string) (path string, raw int64, err error) {
	b, ok := m.writable[name]
	if !ok {
		return "", 0, fmt.Errorf("variable %q: %w", name, sdk.ErrNotSupported)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return "", 0, fmt.Errorf("%s must be a number, got %q: %w", name, value, sdk.ErrInvalid)
	}
	if b.rule.Scale != 0 {
		v /= b.rule.Scale
	}
	f := b.field
	raw = f.Raw(v)
	if f.LogicalMin < f.LogicalMax && math.Abs(f.Physical(raw)-v) > math.Max(0.5, math.Abs(v)*0.01) {
		return "", 0, fmt.Errorf("%s must be between %s and %s: %w", name,
			formatNumber(f.Physical(int64(f.LogicalMin)), nil), formatNumber(f.Physical(int64(f.LogicalMax)), nil), sdk.ErrInvalid)
	}
	return f.Path, raw, nil
}

// fixups correct known device bugs after mapping.
var fixups = map[string]func(vars map[string]string){
	"cps_battery_voltage": func(vars map[string]string) {
		v, err1 := strconv.ParseFloat(vars["battery.voltage"], 64)
		nominal, err2 := strconv.ParseFloat(vars["battery.voltage.nominal"], 64)
		if err1 == nil && err2 == nil && nominal > 0 && v > nominal*1.4 {
			vars["battery.voltage"] = strconv.FormatFloat(v*2/3, 'f', 1, 64)
		}
	},
}
