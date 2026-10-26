// Package nutimport converts a NUT configuration (ups.conf, upsd.conf,
// upsd.users, upsmon.conf) into a currently.yaml.
package nutimport

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/duskglow/currently/internal/nutwire"
)

// Result is the converted configuration.
type Result struct {
	// YAML is the currently.yaml text.
	YAML string
	// Notes explain what was converted, skipped or needs attention.
	Notes []string
}

type section struct {
	name string
	keys map[string]string
	line int
}

// readSections parses ups.conf / upsd.users style files: [name] headers
// followed by key = value lines.
func readSections(path string) ([]*section, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*section
	var cur *section
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end < 0 {
				return nil, fmt.Errorf("%s:%d: unclosed [", path, n)
			}
			cur = &section{name: line[1:end], keys: map[string]string{}, line: n}
			out = append(out, cur)
			continue
		}
		if cur == nil {
			continue // global settings such as maxretry
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			// "upsmon primary" (key and value) or "nolock" (a flag).
			k, v, _ = strings.Cut(line, " ")
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if fields, err := nutwire.Split(v); err == nil && len(fields) > 0 {
			v = strings.Join(fields, " ")
		}
		// upsd.users repeats keys such as "instcmds": keep them all.
		if old, dup := cur.keys[k]; dup && old != "" {
			v = old + " " + v
		}
		cur.keys[k] = v
	}
	return out, sc.Err()
}

// readDirectives parses upsmon.conf / upsd.conf style files: one directive
// and its arguments per line.
func readDirectives(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][]string
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields, err := nutwire.Split(line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		fields[0] = strings.ToUpper(fields[0])
		out = append(out, fields)
	}
	return out, sc.Err()
}

type upsBlock struct {
	name, comment string
	keys          [][2]string // ordered options
}

type userBlock struct {
	name, password, role string
}

// Convert reads the NUT files in dir.
func Convert(dir string) (*Result, error) {
	res := &Result{}
	note := func(format string, args ...any) { res.Notes = append(res.Notes, fmt.Sprintf(format, args...)) }
	path := func(name string) string { return filepath.Join(dir, name) }
	exists := func(name string) bool { _, err := os.Stat(path(name)); return err == nil }
	if !exists("ups.conf") && !exists("upsmon.conf") {
		return nil, fmt.Errorf("no ups.conf or upsmon.conf in %s; is this NUT's configuration directory?", dir)
	}

	// ups.conf: UPSes attached to this machine.
	var upses []*upsBlock
	local := map[string]bool{}
	if exists("ups.conf") {
		sections, err := readSections(path("ups.conf"))
		if err != nil {
			return nil, err
		}
		for _, s := range sections {
			b, why := convertUPS(s)
			if b == nil {
				note("ups.conf [%s]: skipped: %s", s.name, why)
				continue
			}
			upses = append(upses, b)
			local[s.name] = true
		}
	}

	// upsd.conf: where upsd listened.
	var listen []string
	if exists("upsd.conf") {
		dirs, err := readDirectives(path("upsd.conf"))
		if err != nil {
			return nil, err
		}
		for _, d := range dirs {
			switch d[0] {
			case "LISTEN":
				if len(d) >= 2 {
					port := "3493"
					if len(d) >= 3 {
						port = d[2]
					}
					listen = append(listen, net.JoinHostPort(d[1], port))
				}
			case "CERTFILE":
				note("upsd.conf: CERTFILE is set; to keep TLS, set listen.nut_tls with separate cert_file and key_file PEM files")
			}
		}
		if len(listen) > 1 {
			note("upsd.conf: upsd listened on %s; Currently listens on one address, so it uses %s (\":3493\" listens on all)", strings.Join(listen, ", "), listen[0])
		}
	}

	// upsd.users: accounts for NUT clients.
	var users []userBlock
	if exists("upsd.users") {
		sections, err := readSections(path("upsd.users"))
		if err != nil {
			return nil, err
		}
		for _, s := range sections {
			role := userRole(s.keys)
			if role == "" {
				note("upsd.users [%s]: skipped: no upsmon, actions or instcmds, so it could only read, which needs no login in Currently", s.name)
				continue
			}
			users = append(users, userBlock{s.name, s.keys["password"], role})
		}
	}

	// upsmon.conf: which UPSes power this machine, and how to shut down.
	var supplies []string
	var shutdown [][2]string
	monitorOnly := 0
	if exists("upsmon.conf") {
		dirs, err := readDirectives(path("upsmon.conf"))
		if err != nil {
			return nil, err
		}
		for _, d := range dirs {
			switch d[0] {
			case "MONITOR":
				if len(d) < 6 {
					note("upsmon.conf: MONITOR line with %d fields skipped (expected: MONITOR ups@host powervalue user password type)", len(d))
					continue
				}
				name, host := splitSystem(d[1])
				power, _ := strconv.Atoi(d[2])
				if isLocalHost(host) && local[name] {
					if power > 0 {
						supplies = append(supplies, name)
					} else {
						monitorOnly++
					}
					continue
				}
				// A UPS on another server: follow it with the nut driver.
				local := uniqueName(name, upses)
				b := &upsBlock{name: local, comment: fmt.Sprintf("from upsmon.conf: MONITOR %s", d[1])}
				b.keys = append(b.keys, [2]string{"driver", "nut"}, [2]string{"host", host}, [2]string{"ups", name},
					[2]string{"username", d[3]}, [2]string{"password", d[4]})
				upses = append(upses, b)
				if power > 0 {
					supplies = append(supplies, local)
				} else {
					monitorOnly++
				}
			case "MINSUPPLIES":
				shutdown = append(shutdown, [2]string{"min_supplies", arg(d)})
			case "SHUTDOWNCMD":
				if c := arg(d); !isStandardShutdown(c) {
					shutdown = append(shutdown, [2]string{"command", strconv.Quote(c)})
				}
			case "HOSTSYNC":
				shutdown = append(shutdown, [2]string{"wait_for_secondaries", seconds(arg(d))})
			case "FINALDELAY":
				shutdown = append(shutdown, [2]string{"final_delay", seconds(arg(d))})
			case "DEADTIME":
				shutdown = append(shutdown, [2]string{"dead_time", seconds(arg(d))})
			case "POWERDOWNFLAG":
				note("upsmon.conf: POWERDOWNFLAG was set, so NUT told the UPS to cut power at the end of the shutdown; in Currently, set shutdown.power_off_ups: true for that")
			case "NOTIFYCMD", "NOTIFYFLAG", "NOTIFYMSG":
				if d[0] == "NOTIFYCMD" {
					note("upsmon.conf: NOTIFYCMD is not converted; notifications come in a later Currently version (the REST event stream has every event now)")
				}
			}
		}
	}

	if len(upses) == 0 {
		return nil, errors.New("found no UPS that Currently can use")
	}
	if monitorOnly > 0 {
		note("upsmon.conf: %d UPS(es) had power value 0 (monitor only); they are kept, but do not count toward shutting down", monitorOnly)
	}

	// Render.
	var b strings.Builder
	fmt.Fprintf(&b, "# currently configuration converted from the NUT configuration in %s\n", dir)
	fmt.Fprintf(&b, "# on %s by `currently import-nut`. Check it with `currently config validate`\n", time.Now().Format("2006-01-02"))
	b.WriteString("# and `currently test shutdown`.\n\nups:\n")
	for _, u := range upses {
		if u.comment != "" {
			fmt.Fprintf(&b, "  # %s\n", u.comment)
		}
		fmt.Fprintf(&b, "  %s:\n", u.name)
		for _, kv := range u.keys {
			fmt.Fprintf(&b, "    %s: %s\n", kv[0], yamlValue(kv[1]))
		}
	}
	b.WriteString("\nlisten:\n")
	nut := "127.0.0.1:3493"
	if len(listen) > 0 {
		nut = listen[0]
	}
	fmt.Fprintf(&b, "  nut: %q\n", nut)
	if len(users) > 0 {
		b.WriteString("\n# NUT client accounts, from upsd.users. Consider moving the passwords to\n")
		b.WriteString("# environment variables (password_env) or /etc/default/currently.\nusers:\n")
		for _, u := range users {
			fmt.Fprintf(&b, "  %s:\n    password: %s\n    role: %s\n", yamlKey(u.name), strconv.Quote(u.password), u.role)
		}
	}
	if len(supplies) > 0 {
		b.WriteString("\n# From upsmon.conf. As with upsmon, this computer shuts down when a UPS\n")
		b.WriteString("# on battery reports low battery, or when its primary sets FSD.\nshutdown:\n")
		if len(supplies) != len(upses) {
			fmt.Fprintf(&b, "  ups: [%s]\n", strings.Join(supplies, ", "))
		}
		b.WriteString("  when:\n    low_battery: true\n")
		for _, kv := range shutdown {
			fmt.Fprintf(&b, "  %s: %s\n", kv[0], kv[1])
		}
	} else {
		b.WriteString("\n# upsmon.conf had no MONITOR line with a power value, so Currently only monitors.\n")
		note("no shutdown section: upsmon.conf did not say which UPS powers this computer")
	}
	res.YAML = b.String()
	return res, nil
}

// convertUPS maps a ups.conf section, or explains why it cannot.
func convertUPS(s *section) (*upsBlock, string) {
	k := s.keys
	b := &upsBlock{name: s.name}
	switch k["driver"] {
	case "usbhid-ups":
		if d := k["desc"]; d != "" {
			b.keys = append(b.keys, [2]string{"description", strconv.Quote(d)})
		}
		vid, pid := hex4(k["vendorid"]), hex4(k["productid"])
		switch {
		case vid != "" && pid != "":
			b.keys = append(b.keys, [2]string{"id", strconv.Quote(vid + ":" + pid)})
		case vid != "":
			b.keys = append(b.keys, [2]string{"id", strconv.Quote(vid)})
		}
		if v := k["serial"]; v != "" {
			b.keys = append(b.keys, [2]string{"serial", strconv.Quote(v)})
		}
		if v := k["vendor"]; v != "" {
			b.keys = append(b.keys, [2]string{"vendor", strconv.Quote(strings.Trim(v, "^$.*"))})
		}
		if v := k["product"]; v != "" {
			b.keys = append(b.keys, [2]string{"product", strconv.Quote(strings.Trim(v, "^$.*"))})
		}
		if v := k["pollinterval"]; v != "" {
			b.keys = append(b.keys, [2]string{"interval", seconds(v)})
		}
		return b, ""
	case "dummy-ups":
		return nil, "dummy-ups (use the simulator or replay driver instead)"
	case "":
		return nil, "no driver set"
	}
	return nil, fmt.Sprintf("driver %s is not supported by Currently yet (USB HID UPSes are; serial and SNMP drivers come later)", k["driver"])
}

func userRole(k map[string]string) string {
	um := strings.ToLower(k["upsmon"])
	actions := strings.ToUpper(k["actions"])
	switch {
	case k["instcmds"] != "" || strings.Contains(actions, "SET"):
		return "admin"
	case um == "primary" || um == "master" || strings.Contains(actions, "FSD"):
		return "primary"
	case um == "secondary" || um == "slave":
		return "secondary"
	}
	return ""
}

// splitSystem splits "ups@host:port" into the UPS name and host.
func splitSystem(s string) (name, host string) {
	name, host, _ = strings.Cut(s, "@")
	return name, host
}

func isLocalHost(h string) bool {
	host := h
	if hh, _, err := net.SplitHostPort(h); err == nil {
		host = hh
	}
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "", "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func uniqueName(name string, upses []*upsBlock) string {
	taken := map[string]bool{}
	for _, u := range upses {
		taken[u.name] = true
	}
	out := name
	for i := 2; taken[out]; i++ {
		out = name + strconv.Itoa(i)
	}
	return out
}

func arg(d []string) string {
	if len(d) < 2 {
		return ""
	}
	return d[1]
}

func seconds(s string) string {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return strconv.Itoa(n) + "s"
	}
	return strconv.Quote(s)
}

func hex4(s string) string {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "0x"))
	if s == "" {
		return ""
	}
	if n, err := strconv.ParseUint(s, 16, 16); err == nil {
		return fmt.Sprintf("%04x", n)
	}
	return ""
}

func isStandardShutdown(c string) bool {
	c = strings.TrimSpace(c)
	for _, std := range []string{"/sbin/shutdown -h +0", "/sbin/shutdown -h now", "shutdown -h +0", "shutdown -h now",
		"/sbin/shutdown -p now", "/usr/sbin/shutdown -h +0", "systemctl poweroff", "/bin/systemctl poweroff"} {
		if c == std {
			return true
		}
	}
	return false
}

// yamlValue returns values already quoted or numeric as-is.
func yamlValue(v string) string {
	if strings.HasPrefix(v, `"`) {
		return v
	}
	if _, err := strconv.ParseFloat(strings.TrimSuffix(v, "s"), 64); err == nil {
		return v
	}
	if v == "nut" || v == "true" || v == "false" {
		return v
	}
	return strconv.Quote(v)
}

func yamlKey(k string) string {
	for _, r := range k {
		if r != '_' && r != '-' && r != '.' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return strconv.Quote(k)
		}
	}
	return k
}

// SortedNotes returns notes sorted for stable output.
func (r *Result) SortedNotes() []string {
	out := append([]string(nil), r.Notes...)
	sort.Strings(out)
	return out
}
