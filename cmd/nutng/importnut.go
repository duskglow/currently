package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/nut-ng/nut-ng/internal/config"
	"github.com/nut-ng/nut-ng/internal/nutimport"
	"github.com/nut-ng/nut-ng/internal/registry"
)

func (a *app) importNUTCmd() *cobra.Command {
	var output string
	var force bool
	cmd := &cobra.Command{
		Use:   "import-nut [nut-config-dir]",
		Short: "Convert an existing NUT configuration into nutng.yaml",
		Long: `Read NUT's ups.conf, upsd.conf, upsd.users and upsmon.conf (from /etc/nut
by default) and write the equivalent nutng.yaml.

USB HID UPSes (usbhid-ups) become usbhid UPSes, MONITOR lines for other
servers become nut-driver UPSes, upsd.users become users, and upsmon's
settings become the shutdown policy. Anything that cannot be converted
is listed. The NUT files are not changed.

Without --output, the result is printed.`,
		Example: `  nutng import-nut                       # print the converted /etc/nut
  sudo nutng import-nut -o /etc/nutng/nutng.yaml`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "/etc/nut"
			if runtime.GOOS == "windows" {
				dir = `C:\Program Files\NUT\etc`
			}
			if len(args) == 1 {
				dir = args[0]
			}
			res, err := nutimport.Convert(dir)
			if err != nil {
				return err
			}
			c, err := config.Parse("converted", []byte(res.YAML))
			if err == nil {
				err = c.Validate(registry.Builtin())
			}
			if err != nil {
				return fmt.Errorf("the converted config has problems, please report this: %w\n%s", err, res.YAML)
			}

			out := cmd.OutOrStdout()
			if output == "" {
				fmt.Fprint(out, res.YAML)
			} else {
				if _, err := os.Stat(output); err == nil && !force {
					return fmt.Errorf("%s already exists; use --force to replace it", output)
				}
				if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
					return permissionHint(err, output)
				}
				// The file holds the passwords from upsd.users and upsmon.conf.
				if err := os.WriteFile(output, []byte(res.YAML), 0o600); err != nil {
					return permissionHint(err, output)
				}
				fmt.Fprintf(out, "Wrote %s (readable only by its owner: it contains passwords).\n", output)
			}
			if len(res.Notes) > 0 {
				fmt.Fprintln(a.stderr, "\nNotes:")
				for _, n := range res.SortedNotes() {
					fmt.Fprintln(a.stderr, "  - "+n)
				}
			}
			if output != "" {
				fmt.Fprintln(out, "\nNext: `nutng test shutdown` to check the shutdown policy, then stop NUT and start nutng.")
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "write nutng.yaml here instead of printing it")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing file")
	return cmd
}
