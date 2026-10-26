package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nut-ng/nut-ng/internal/config"
	"github.com/nut-ng/nut-ng/internal/paths"
	"github.com/nut-ng/nut-ng/internal/plugins"
	"github.com/nut-ng/nut-ng/internal/scaffold"
	"github.com/nut-ng/nut-ng/internal/term"
	"github.com/nut-ng/nut-ng/internal/version"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

func (a *app) pluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "List, inspect and create driver and output plugins",
		Long: `Plugins add drivers (new kinds of UPS) and outputs (new places to send
UPS data) without rebuilding nutng. A plugin is an executable named
nutng-driver-<name> or nutng-output-<name> in a plugin directory, or a
command listed under plugins.commands in nutng.yaml (for interpreted
languages such as Python).`,
	}
	cmd.AddCommand(a.pluginListCmd(), a.pluginInfoCmd(), a.pluginNewCmd())
	return cmd
}

func (a *app) pluginDirs(cfg *config.Config) []string {
	return append(append([]string{}, cfg.Plugins.Dirs...), paths.PluginDirs()...)
}

func (a *app) pluginListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed plugins and where nutng looks for them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := a.loadConfig()
			if err != nil {
				cfg = config.Default()
			}
			_, pm := a.registry(cmd.Context(), cfg)
			defer pm.Close()
			st := pm.Statuses()
			out := cmd.OutOrStdout()
			if asJSON {
				if st == nil {
					st = []plugins.Status{}
				}
				return writeJSON(out, st)
			}
			p := a.painter()
			if len(st) == 0 {
				fmt.Fprintln(out, "No plugins installed.")
			} else {
				t := &table{p: p}
				t.addText("NAME", "KIND", "VERSION", "STATE", "SOURCE")
				for _, s := range st {
					state, color := "ok", term.Green
					if s.Error != "" {
						state, color = "error: "+truncate(s.Error, 60), term.Red
					}
					t.add(cell{text: orDash(s.Name), color: term.Bold}, cell{text: orDash(string(s.Kind))},
						cell{text: orDash(s.Version)}, cell{text: state, color: color}, cell{text: s.Source, color: term.Dim})
				}
				t.write(out)
			}
			fmt.Fprintln(out, "\nPlugin directories, searched in order:")
			for _, d := range a.pluginDirs(cfg) {
				mark := p.paint(term.Dim, "(missing)")
				if fi, err := os.Stat(d); err == nil && fi.IsDir() {
					mark = ""
				}
				fmt.Fprintf(out, "  %s %s\n", d, mark)
			}
			if len(cfg.Plugins.Commands) > 0 {
				fmt.Fprintln(out, "Plugin commands from nutng.yaml:")
				for _, c := range cfg.Plugins.Commands {
					fmt.Fprintf(out, "  %s\n", strings.Join(c, " "))
				}
			}
			fmt.Fprintln(out, "\nStart a new one with `nutng plugin new driver|output <name>`.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func (a *app) pluginInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <name>",
		Short: "Show a plugin's details, and for drivers, the devices it finds",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, pm := a.registry(cmd.Context(), nil)
			defer pm.Close()
			out := cmd.OutOrStdout()
			for _, s := range pm.Statuses() {
				if s.Name != args[0] {
					continue
				}
				fmt.Fprintf(out, "%s (%s plugin, version %s)\n  %s\n  Source: %s\n", s.Name, s.Kind, orDash(s.Version), s.Description, s.Source)
				if s.Error != "" {
					fmt.Fprintf(out, "  Error: %s\n", s.Error)
					return exitError(1)
				}
				if s.Kind == sdk.KindDriver {
					d, _ := reg.Driver(s.Name)
					found, err := d.Discover(cmd.Context())
					switch {
					case err != nil:
						fmt.Fprintf(out, "  Discovery failed: %v\n", err)
					case len(found) == 0:
						fmt.Fprintln(out, "  Finds no devices by itself; configure them in nutng.yaml.")
					default:
						fmt.Fprintln(out, "  Devices found:")
						for _, f := range found {
							fmt.Fprintf(out, "    %s  %v\n", f.Description, f.Config)
						}
					}
				}
				return nil
			}
			return fmt.Errorf("no plugin named %q; `nutng plugin list` shows the installed ones", args[0])
		},
	}
}

func (a *app) pluginNewCmd() *cobra.Command {
	var lang, dir, sdkPath string
	cmd := &cobra.Command{
		Use:   "new driver|output <name>",
		Short: "Create a new plugin project in Go or Python",
		Example: `  nutng plugin new driver myups              # Go, in ./nutng-driver-myups
  nutng plugin new output webhook --lang python`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, name := args[0], args[1]
			if dir == "" {
				dir = "nutng-" + kind + "-" + name
			}
			p := scaffold.Params{Kind: kind, Name: name, Lang: lang, SDKPath: sdkPath}
			if v := version.Version; strings.HasPrefix(v, "v") {
				p.NutngVersion = v
			}
			written, err := scaffold.Create(dir, p)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Created %s (%d files).\n\nNext:\n", dir, len(written))
			exe := "nutng-" + kind + "-" + name
			if runtime.GOOS == "windows" {
				exe += ".exe"
			}
			if lang == "go" {
				fmt.Fprintf(out, "  cd %s\n  go mod tidy\n  go build -o %s\n", dir, exe)
				fmt.Fprintln(out, "  copy it into a plugin directory (`nutng plugin list` shows them)")
			} else {
				fmt.Fprintf(out, "  cd %s\n  python3 -m pip install -r requirements.txt\n", dir)
				fmt.Fprintln(out, "  add the plugins.commands line from the top of plugin.py to nutng.yaml")
			}
			fmt.Fprintf(out, "  nutng plugin info %s\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&lang, "lang", "go", "language: go or python")
	cmd.Flags().StringVarP(&dir, "dir", "o", "", "directory to create (default nutng-<kind>-<name>)")
	cmd.Flags().StringVar(&sdkPath, "sdk", "", "Go: build against this local nut-ng checkout")
	return cmd
}
