package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nut-ng/nut-ng/drivers/usbhid"
	"github.com/nut-ng/nut-ng/internal/config"
	"github.com/nut-ng/nut-ng/internal/registry"
	"github.com/nut-ng/nut-ng/internal/term"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// found is a discovered UPS with a suggested name.
type found struct {
	// UPS is the suggested UPS name for nutng.yaml.
	UPS string `json:"ups"`
	sdk.Discovered
}

// discover asks every driver for devices and names them.
func discover(ctx context.Context, reg *registry.Registry, taken map[string]bool) ([]found, []error) {
	var out []found
	var errs []error
	for _, d := range reg.Drivers() {
		list, err := d.Discover(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d.Info().Name, err))
			continue
		}
		for _, x := range list {
			out = append(out, found{UPS: uniqueName(suggestName(x), taken), Discovered: x})
		}
	}
	return out, errs
}

var vendorNames = map[string]string{
	"051d": "apc", "0764": "cyberpower", "0501": "cyberpower", "0463": "eaton",
	"09ae": "tripplite", "10af": "liebert", "050d": "belkin", "0d9f": "powercom",
}

func suggestName(d sdk.Discovered) string {
	if id, ok := d.Config["id"].(string); ok && len(id) >= 4 {
		if n, ok := vendorNames[strings.ToLower(id[:4])]; ok {
			return n
		}
	}
	return "ups"
}

func uniqueName(base string, taken map[string]bool) string {
	name := base
	for i := 2; taken[name]; i++ {
		name = base + strconv.Itoa(i)
	}
	taken[name] = true
	return name
}

// upsYAML renders a UPS block for nutng.yaml.
func upsYAML(f found) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s:\n", f.UPS)
	if f.Name != "" {
		fmt.Fprintf(&b, "    description: %s\n", strconv.Quote(f.Name))
	}
	if f.Driver != config.DefaultDriver {
		fmt.Fprintf(&b, "    driver: %s\n", f.Driver)
	}
	keys := make([]string, 0, len(f.Config))
	for k := range f.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// With a serial, the vendor/product ID adds nothing.
		if k == "id" && f.Config["serial"] != nil {
			continue
		}
		fmt.Fprintf(&b, "    %s: %s\n", k, strconv.Quote(fmt.Sprint(f.Config[k])))
	}
	return b.String()
}

func (a *app) detectCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "detect",
		Short: "List the UPSes nutng can find",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, errs := discover(cmd.Context(), registry.Builtin(), map[string]bool{})
			out := cmd.OutOrStdout()
			if asJSON {
				if list == nil {
					list = []found{}
				}
				return writeJSON(out, list)
			}
			for _, err := range errs {
				fmt.Fprintln(a.stderr, "nutng:", err)
			}
			if len(list) == 0 {
				fmt.Fprintln(out, "No UPS found.")
				fmt.Fprintln(out, usbhid.NoDeviceHelp())
				return exitError(1)
			}
			p := a.painter()
			fmt.Fprintf(out, "Found %d UPS:\n", len(list))
			for _, f := range list {
				fmt.Fprintf(out, "  %s  %s\n", f.Description, p.paint(term.Dim, "("+f.Driver+" driver)"))
			}
			fmt.Fprintln(out, "\nTo monitor it, run `nutng init`, or add this to nutng.yaml:\n\nups:")
			for _, f := range list {
				fmt.Fprint(out, upsYAML(f))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func (a *app) initCmd() *cobra.Command {
	var yes, network, force, noShutdown bool
	var output string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Find your UPS and write a config file",
		Long: `Find attached UPSes, ask a few questions, and write nutng.yaml.

Use --yes to accept the defaults without questions: every UPS found, the
REST API on this machine only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := output
			if path == "" {
				path = a.configPath
			}
			if path == "" {
				path = config.DefaultPath()
			}
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists; edit it, or use --force to replace it", path)
			}
			out := cmd.OutOrStdout()
			interactive := !yes && a.stdin == os.Stdin && term.IsTerminal(os.Stdin)
			ask := &prompter{in: bufio.NewReader(a.stdin), out: out}

			list, errs := discover(cmd.Context(), registry.Builtin(), map[string]bool{})
			for _, err := range errs {
				fmt.Fprintln(a.stderr, "nutng:", err)
			}
			var chosen []found
			demo := false
			switch {
			case len(list) == 0:
				fmt.Fprintln(out, "No UPS found. "+usbhid.NoDeviceHelp())
				if interactive && !ask.yesNo("Write a config with a simulated UPS for now?", true) {
					return exitError(1)
				}
				demo = true
			case !interactive:
				chosen = list
			default:
				fmt.Fprintf(out, "Found %d UPS:\n", len(list))
				for i, f := range list {
					fmt.Fprintf(out, "  %d) %s\n", i+1, f.Description)
				}
				chosen = list
				if len(list) > 1 {
					chosen = ask.choose("Which should nutng monitor? Numbers separated by commas, or Enter for all", list)
				}
				taken := map[string]bool{}
				for i := range chosen {
					chosen[i].UPS = ask.name(fmt.Sprintf("Name for %s", chosen[i].Description), chosen[i].UPS, taken)
				}
			}
			shutdown := !noShutdown
			if interactive {
				network = ask.yesNo("Should monitoring tools and NUT clients on other machines reach nutng?", network)
				shutdown = ask.yesNo("Shut this computer down when the UPS battery runs low?", shutdown)
			}

			text := renderConfig(chosen, demo, network, shutdown)
			if _, err := config.Parse(path, []byte(text)); err != nil {
				return fmt.Errorf("internal error: generated config is invalid: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return permissionHint(err, path)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				return permissionHint(err, path)
			}
			fmt.Fprintf(out, "\nWrote %s.\nStart nutng with:  nutng serve", path)
			if path != config.DefaultPath() {
				fmt.Fprintf(out, " --config %s", strconv.Quote(path))
			}
			fmt.Fprintln(out, "\nThen check it with: nutng status")
			if shutdown {
				fmt.Fprintln(out, "See what a shutdown would do (without doing it): nutng test shutdown")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&yes, "yes", "y", false, "accept the defaults without asking")
	f.BoolVar(&network, "network", false, "serve the API on all network interfaces, not just this machine")
	f.BoolVar(&force, "force", false, "replace an existing config file")
	f.BoolVar(&noShutdown, "no-shutdown", false, "only monitor; never shut this computer down")
	f.StringVarP(&output, "output", "o", "", "where to write the config (default: --config, or "+config.DefaultPath()+")")
	return cmd
}

func renderConfig(chosen []found, demo, network, shutdown bool) string {
	var b strings.Builder
	b.WriteString("# nutng configuration, written by `nutng init`.\n")
	b.WriteString("# Check changes with `nutng config validate`.\n\nups:\n")
	for _, f := range chosen {
		b.WriteString(upsYAML(f))
	}
	if demo {
		b.WriteString("  # No UPS was found, so this simulated one lets you try nutng.\n")
		b.WriteString("  # Replace it with `nutng detect` output once the UPS is connected.\n")
		b.WriteString("  demo:\n    driver: simulator\n    scenario: cycle\n")
	}
	b.WriteString("\nlisten:\n")
	if network {
		b.WriteString("  http: \":9300\"             # REST API on all interfaces (read-only without a token)\n")
		b.WriteString("  nut: \":3493\"              # NUT protocol on all interfaces\n")
	} else {
		b.WriteString("  http: \"" + config.DefaultHTTP + "\"   # REST API; use \":9300\" for other machines\n")
		b.WriteString("  nut: \"" + config.DefaultNUT + "\"    # NUT protocol; use \":3493\" for other machines\n")
	}
	b.WriteString("\n# api:\n#   token_env: NUTNG_TOKEN   # allow commands over HTTP with this bearer token\n")
	if shutdown {
		b.WriteString("\n# Shut this computer down when a UPS on battery is about to run out.\n")
		b.WriteString("# Check it with `nutng test shutdown`; it never shuts down for a simulated UPS.\n")
		b.WriteString("shutdown:\n  when:\n    low_battery: true       # the UPS's own low-battery signal\n")
		b.WriteString("    # on_battery_for: 5m     # or after this long on battery\n")
		b.WriteString("    # runtime_below: 5m      # or when less runtime than this is left\n")
		b.WriteString("  # hooks:                  # commands to run first, such as stopping VMs\n")
		b.WriteString("  #   - /usr/local/bin/stop-vms\n")
	} else {
		b.WriteString("\n# No shutdown section: nutng monitors only. See `nutng test shutdown`.\n")
	}
	return b.String()
}

func permissionHint(err error, path string) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("cannot write %s: permission denied. Run as root/Administrator, or choose another file with --output ./nutng.yaml", path)
	}
	return err
}

// prompter asks questions on the terminal.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func (p *prompter) line(question, def string) string {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", question, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", question)
	}
	s, _ := p.in.ReadString('\n')
	if s = strings.TrimSpace(s); s == "" {
		return def
	}
	return s
}

func (p *prompter) yesNo(question string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	for {
		switch strings.ToLower(p.line(question+" ("+d+")", "")) {
		case "":
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
	}
}

func (p *prompter) choose(question string, list []found) []found {
	for {
		s := p.line(question, "")
		if s == "" {
			return list
		}
		var out []found
		ok := true
		for _, part := range strings.Split(s, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || n < 1 || n > len(list) {
				ok = false
				break
			}
			out = append(out, list[n-1])
		}
		if ok && len(out) > 0 {
			return out
		}
		fmt.Fprintf(p.out, "Please enter numbers between 1 and %d.\n", len(list))
	}
}

func (p *prompter) name(question, def string, taken map[string]bool) string {
	def = uniqueName(def, taken)
	delete(taken, def)
	for {
		n := p.line(question, def)
		if config.ValidUPSName(n) && !taken[n] {
			taken[n] = true
			return n
		}
		fmt.Fprintln(p.out, "Use letters, digits, '.', '_' or '-', starting with a letter or digit, and a name not used yet.")
	}
}
