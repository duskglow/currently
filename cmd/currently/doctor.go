package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/duskglow/currently/drivers/nutclient"
	"github.com/duskglow/currently/drivers/usbhid"
	"github.com/duskglow/currently/internal/client"
	"github.com/duskglow/currently/internal/config"
	"github.com/duskglow/currently/internal/plugins"
	"github.com/duskglow/currently/internal/policy"
	"github.com/duskglow/currently/internal/registry"
	"github.com/duskglow/currently/internal/svcmgr"
	"github.com/duskglow/currently/internal/term"
	"github.com/duskglow/currently/internal/version"
	"github.com/duskglow/currently/outputs/snmp"
	"github.com/duskglow/currently/pkg/sdk"
)

// Check results.
const (
	checkOK   = "ok"
	checkInfo = "info"
	checkWarn = "warn"
	checkFail = "fail"
)

type check struct {
	Status string   `json:"status"`
	Area   string   `json:"area"`
	Detail string   `json:"detail"`
	Fix    []string `json:"fix,omitempty"`
}

type doctor struct {
	app     *app
	ctx     context.Context
	checks  []check
	reg     *registry.Registry
	plugins *plugins.Manager
}

func (d *doctor) add(status, area, detail string, fix ...string) {
	d.checks = append(d.checks, check{status, area, detail, fix})
}

func (a *app) doctorCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the currently setup and explain how to fix problems",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := &doctor{app: a, ctx: cmd.Context()}
			d.run()
			out := cmd.OutOrStdout()
			if asJSON {
				if err := writeJSON(out, d.checks); err != nil {
					return err
				}
			} else {
				d.print(out)
			}
			for _, c := range d.checks {
				if c.Status == checkFail {
					return exitError(1)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func (d *doctor) run() {
	cfg := d.checkConfig()
	d.checkPlugins()
	svc := d.checkService()
	list, reachable := d.checkDaemon(cfg, svc)
	d.checkPorts(cfg, reachable)
	d.checkSNMP(cfg, reachable)
	d.checkUSB(cfg)
	if reachable {
		d.checkUPSes(list)
	}
	d.checkShutdown(cfg, list)
	d.checkNUT()
}

func (d *doctor) checkConfig() *config.Config {
	cfg, found, err := d.app.loadConfig()
	switch {
	case err != nil:
		d.add(checkFail, "Config", err.Error(), "fix the lines shown, then run `currently config validate`")
		d.reg, d.plugins = d.app.registry(d.ctx, config.Default())
		return config.Default()
	case !found:
		d.add(checkWarn, "Config", fmt.Sprintf("no config file at %s; Currently monitors every USB UPS it finds", config.DefaultPath()),
			"run `currently init` to write one")
		d.reg, d.plugins = d.app.registry(d.ctx, cfg)
		return cfg
	}
	d.reg, d.plugins = d.app.registry(d.ctx, cfg)
	if err := cfg.Validate(d.reg); err != nil {
		d.add(checkFail, "Config", err.Error(), "fix the lines shown, then run `currently config validate`")
		return cfg
	}
	names := cfg.UPSNames()
	d.add(checkOK, "Config", fmt.Sprintf("%s is valid: %d UPS (%s)", cfg.Path, len(names), strings.Join(names, ", ")))
	return cfg
}

func (d *doctor) checkService() svcmgr.State {
	st, err := svcmgr.Status()
	admin := "with sudo"
	if runtime.GOOS == "windows" {
		admin = "in an Administrator terminal"
	}
	switch {
	case err != nil:
		d.add(checkWarn, "Service", "could not check the service: "+err.Error())
	case st == svcmgr.StateRunning:
		d.add(checkOK, "Service", fmt.Sprintf("installed and running (%s); logs: %s", svcmgr.Platform(), svcmgr.LogHint()))
	case st == svcmgr.StateStopped:
		d.add(checkWarn, "Service", "installed but stopped", "run `currently service start` "+admin)
	case st == svcmgr.StateNotInstalled:
		d.add(checkWarn, "Service", "not installed, so Currently runs only while `currently serve` is open",
			"run `currently service install` "+admin+" to start Currently at boot")
	default:
		d.add(checkWarn, "Service", "state unknown")
	}
	return st
}

func (d *doctor) checkDaemon(cfg *config.Config, svc svcmgr.State) ([]sdk.UPS, bool) {
	c, err := d.app.client()
	if err == nil {
		ctx, cancel := context.WithTimeout(d.ctx, 3*time.Second)
		defer cancel()
		var list []sdk.UPS
		if list, err = c.ListUPS(ctx); err == nil {
			detail := "running"
			if c.ReadOnlyBecause != "" {
				detail += " (read-only access: " + c.ReadOnlyBecause + ")"
			}
			d.add(checkOK, "Daemon", detail)
			return list, true
		}
	}
	fix := "run `currently serve`, or install the service with `currently service install`"
	switch svc {
	case svcmgr.StateStopped:
		fix = "start the service with `currently service start`"
	case svcmgr.StateRunning:
		fix = "the service is running but not answering; check its logs (" + svcmgr.LogHint() + ")"
	}
	detail := "not reachable: " + err.Error()
	if errors.Is(err, client.ErrNotRunning) {
		detail = "not running"
	}
	d.add(checkFail, "Daemon", detail, fix)
	return nil, false
}

// checkPorts makes sure Currently's ports are free (daemon not running) or
// answering (daemon running).
func (d *doctor) checkPorts(cfg *config.Config, running bool) {
	type port struct{ key, addr, what, usual string }
	var ports []port
	if cfg.Listen.HTTP != "" {
		ports = append(ports, port{"listen.http", cfg.Listen.HTTP, "REST API", "another program"})
	}
	if cfg.Listen.NUT != "" {
		ports = append(ports, port{"listen.nut", cfg.Listen.NUT, "NUT protocol", "NUT's upsd"})
	}
	if m := cfg.Listen.Metrics; m != "" && !strings.EqualFold(m, "off") {
		ports = append(ports, port{"listen.metrics", m, "Prometheus metrics", "another program"})
	}
	for _, p := range ports {
		if !running {
			ln, err := net.Listen("tcp", p.addr)
			if err != nil {
				fix := []string{fmt.Sprintf("stop the program using it, or set %s in currently.yaml to another address or \"off\"", p.key)}
				if p.key == "listen.nut" && runtime.GOOS == "linux" {
					fix = append([]string{"if NUT is installed: `sudo systemctl disable --now nut-server`"}, fix...)
				}
				d.add(checkFail, "Ports", fmt.Sprintf("%s port %s is in use (probably %s)", p.what, p.addr, p.usual), fix...)
				continue
			}
			ln.Close()
			d.add(checkOK, "Ports", fmt.Sprintf("%s port %s is free", p.what, p.addr))
			continue
		}
		if err := probe(p.key, client.LocalURL(p.addr)); err != nil {
			d.add(checkFail, "Ports", fmt.Sprintf("%s on %s does not answer: %v", p.what, p.addr, err),
				"check Currently's log for errors about "+p.key)
		} else {
			d.add(checkOK, "Ports", fmt.Sprintf("%s answers on %s", p.what, p.addr))
		}
	}
}

// checkSNMP is checkPorts for the SNMP agent's UDP port.
func (d *doctor) checkSNMP(cfg *config.Config, running bool) {
	s := cfg.Listen.SNMP
	if s == nil {
		return
	}
	if !running {
		pc, err := net.ListenPacket("udp", s.Addr)
		if err != nil {
			fix := "stop the program using it (often snmpd), or set listen.snmp.addr in currently.yaml to another address"
			if errors.Is(err, os.ErrPermission) {
				fix = "ports below 1024 need root; use a port such as 1161, or run Currently as root (the default service)"
			}
			d.add(checkFail, "Ports", fmt.Sprintf("SNMP port %s (UDP) cannot be used: %v", s.Addr, err), fix)
			return
		}
		pc.Close()
		d.add(checkOK, "Ports", fmt.Sprintf("SNMP port %s (UDP) is free", s.Addr))
		return
	}
	addr := strings.TrimPrefix(client.LocalURL(s.Addr), "http://")
	if _, err := snmp.Probe(addr, 2*time.Second); err != nil {
		d.add(checkFail, "Ports", fmt.Sprintf("SNMP agent on %s does not answer: %v", s.Addr, err),
			"check Currently's log for errors about listen.snmp, and that no firewall blocks UDP "+s.Addr)
	} else {
		d.add(checkOK, "Ports", "SNMP agent answers on "+s.Addr+" (UDP)")
	}
}

func probe(key, url string) error {
	if key == "listen.nut" {
		addr := strings.TrimPrefix(url, "http://")
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := fmt.Fprint(conn, "NETVER\n"); err != nil {
			return err
		}
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			return err
		}
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "ERR") {
			return fmt.Errorf("unexpected reply %q", strings.TrimSpace(line))
		}
		return nil
	}
	path := "/healthz"
	if key == "listen.metrics" {
		path = "/metrics"
	}
	hc := &http.Client{Timeout: 2 * time.Second}
	resp, err := hc.Get(url + path)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	return nil
}

func (d *doctor) checkUSB(cfg *config.Config) {
	usesUSB := len(cfg.UPS) == 0
	for _, u := range cfg.UPS {
		if u.Driver == usbhid.DriverName {
			usesUSB = true
		}
	}
	upses, err := usbhid.New().UPSes()
	switch {
	case err != nil:
		d.add(checkWarn, "USB", "could not list USB devices: "+err.Error())
		return
	case len(upses) == 0 && usesUSB:
		d.add(checkWarn, "USB", "no USB UPS found", usbhid.NoDeviceHelp())
		return
	case len(upses) == 0:
		d.add(checkInfo, "USB", "no USB UPS found (none configured)")
		return
	}
	for _, u := range upses {
		if u.Problem != "" {
			d.add(checkFail, "USB", "found "+u.Description()+", but "+u.Problem, usbhid.NodeHelp(u))
			continue
		}
		d.add(checkOK, "USB", "found "+u.Description())
		if runtime.GOOS == "linux" && os.Geteuid() != 0 {
			if f, err := os.OpenFile(u.Path, os.O_RDWR, 0); err != nil {
				d.add(checkWarn, "USB", fmt.Sprintf("this user cannot open %s (%v); that is fine when Currently runs as root (the default service)", u.Path, err),
					"to run Currently as this user, install packaging/linux/90-currently-ups.rules and join the currently group")
			} else {
				f.Close()
			}
		}
	}
}

func (d *doctor) checkUPSes(list []sdk.UPS) {
	for _, u := range list {
		switch u.State {
		case sdk.StateConnected:
			msg := fmt.Sprintf("%s: %s", u.Name, u.Status().Describe())
			if c := u.Value("battery.charge"); c != "" {
				msg += ", battery " + c + "%"
			}
			d.add(checkOK, "UPS", msg)
		case sdk.StateConnecting:
			d.add(checkWarn, "UPS", u.Name+": still connecting")
		default:
			fix := "run `currently detect` to see which UPSes Currently can find, and check the UPS block in currently.yaml"
			if strings.Contains(u.LastError, "permission denied") {
				fix = "install packaging/linux/90-currently-ups.rules, or run Currently as root"
			}
			d.add(checkFail, "UPS", fmt.Sprintf("%s: %s: %s", u.Name, u.State, u.LastError), fix)
		}
	}
}

// checkPlugins reports plugins that failed to start.
func (d *doctor) checkPlugins() {
	defer d.plugins.Close()
	for _, s := range d.plugins.Statuses() {
		if s.Error != "" {
			d.add(checkFail, "Plugins", fmt.Sprintf("%s: %s", s.Source, s.Error),
				"check that it is a Currently plugin built for this Currently version; `currently plugin list` shows the details")
			continue
		}
		d.add(checkOK, "Plugins", fmt.Sprintf("%s %s %s (%s)", s.Kind, s.Name, s.Version, s.Source))
	}
}

// checkShutdown reviews the shutdown policy.
func (d *doctor) checkShutdown(cfg *config.Config, list []sdk.UPS) {
	sd := cfg.Shutdown
	if sd == nil {
		d.add(checkWarn, "Shutdown", "no shutdown policy: Currently will not shut this computer down when the battery runs out",
			"add a shutdown section to currently.yaml; `currently test shutdown` shows an example")
		return
	}
	problems := false
	if sd.DryRun {
		d.add(checkWarn, "Shutdown", "dry_run is on: Currently logs each shutdown step but does none of them",
			"remove dry_run from the shutdown section when you are done testing")
		problems = true
	}
	if sd.Command == "" {
		if why := policy.BuiltinBlocked(); why != "" {
			d.add(checkWarn, "Shutdown", "the operating system shutdown command is blocked ("+why+")",
				"unset "+policy.NeverShutdownEnv+" when you are done testing")
			problems = true
		}
		if _, err := exec.LookPath(policy.DefaultCommand()[0]); err != nil {
			d.add(checkFail, "Shutdown", fmt.Sprintf("the shutdown command %s was not found", policy.DefaultCommand()[0]),
				"set shutdown.command to this system's shutdown command")
			problems = true
		}
	}
	commands := map[string]map[string]bool{}
	for _, u := range list {
		commands[u.Name] = map[string]bool{}
		for _, c := range u.Commands {
			commands[u.Name][c.Name] = true
		}
	}
	simulated := true
	for _, name := range cfg.Supplies() {
		u := cfg.UPS[name]
		if u.Driver != "simulator" && u.Driver != "replay" {
			simulated = false
		}
		if u.Driver == nutclient.DriverName && u.Options["username"] == nil {
			d.add(checkWarn, "Shutdown", fmt.Sprintf("UPS %s has no username, so its primary will not wait for this computer before cutting power", name),
				"add username and password (a secondary user on that server) to the "+name+" block")
			problems = true
		}
		if sd.PowerOffUPS && u.Driver != nutclient.DriverName {
			if cmds, ok := commands[name]; ok && !cmds["shutdown.return"] {
				d.add(checkWarn, "Shutdown", fmt.Sprintf("power_off_ups is on, but UPS %s does not support shutdown.return", name),
					"turn power_off_ups off, or check the UPS with `currently cmd "+name+"`")
				problems = true
			}
		}
	}
	switch {
	case simulated && sd.Command == "":
		d.add(checkInfo, "Shutdown", "every UPS powering this computer is simulated, so a shutdown is only a rehearsal")
		return
	case simulated:
		d.add(checkInfo, "Shutdown", "every UPS powering this computer is simulated; in a simulated outage the hooks and your shutdown.command still run")
		return
	}
	if !problems {
		d.add(checkOK, "Shutdown", fmt.Sprintf("armed for %s; `currently test shutdown` shows every step", strings.Join(cfg.Supplies(), ", ")))
	}
}

// checkNUT looks for a NUT installation that would compete for the UPS or
// port 3493.
func (d *doctor) checkNUT() {
	if runtime.GOOS != "linux" {
		return
	}
	var running []string
	procs, _ := filepath.Glob("/proc/[0-9]*/comm")
	seen := map[string]bool{}
	for _, p := range procs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(string(b))
		if (name == "upsd" || name == "upsmon" || strings.HasSuffix(name, "-ups") || name == "upsdrvctl") && !seen[name] {
			seen[name] = true
			running = append(running, name)
		}
	}
	if len(running) > 0 {
		d.add(checkWarn, "NUT", "NUT is running ("+strings.Join(running, ", ")+"); its upsd uses port 3493 and its driver the UPS",
			"to switch to currently: `sudo systemctl disable --now nut-server nut-monitor nut-driver.target`")
	}
	if _, err := os.Stat("/etc/nut/ups.conf"); err == nil {
		d.add(checkInfo, "NUT", "found a NUT configuration in /etc/nut")
	}
}

func (d *doctor) print(w io.Writer) {
	p := d.app.painter()
	fmt.Fprintf(w, "currently doctor (%s, %s/%s)\n\n", version.String(), runtime.GOOS, runtime.GOARCH)
	labels := map[string]struct{ text, color string }{
		checkOK: {"OK  ", term.Green}, checkInfo: {"INFO", term.Dim}, checkWarn: {"WARN", term.Yellow}, checkFail: {"FAIL", term.Red},
	}
	fails, warns := 0, 0
	for _, c := range d.checks {
		l := labels[c.Status]
		lines := strings.Split(c.Detail, "\n")
		fmt.Fprintf(w, "  %s  %-8s %s\n", p.paint(l.color, l.text), c.Area, lines[0])
		for _, more := range lines[1:] {
			fmt.Fprintf(w, "  %s  %-8s %s\n", "    ", "", more)
		}
		for _, f := range c.Fix {
			fmt.Fprintf(w, "  %s  %-8s %s\n", "    ", "", p.paint(term.Cyan, "→ "+f))
		}
		switch c.Status {
		case checkFail:
			fails++
		case checkWarn:
			warns++
		}
	}
	fmt.Fprintln(w)
	switch {
	case fails == 0 && warns == 0:
		fmt.Fprintln(w, "Everything looks good.")
	default:
		fmt.Fprintf(w, "%d problem(s), %d warning(s).\n", fails, warns)
	}
}
