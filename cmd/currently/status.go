package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/duskglow/currently/internal/client"
	"github.com/duskglow/currently/internal/term"
)

func (a *app) painter() painter {
	return painter{on: a.stdout == os.Stdout && term.UseColor(os.Stdout)}
}

// printShutdownLine prints the shutdown policy's state, if the daemon
// reports it.
func (a *app) printShutdownLine(ctx context.Context, c *client.Client, out io.Writer) {
	st, err := c.Shutdown(ctx)
	if err != nil {
		return
	}
	line, color := shutdownLine(st)
	fmt.Fprintf(out, "  %-11s %s\n", "Shutdown", a.painter().paint(color, line))
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

const noUPSHelp = `No UPS is configured.
Add one to %s, or run ` + "`currently serve --demo`" + ` to try a simulated UPS.
`

func (a *app) statusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status [ups]",
		Short: "Show the state of all UPSes, or one in detail",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				u, err := c.GetUPS(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(out, u)
				}
				writeDetail(out, a.painter(), u, time.Now())
				a.printShutdownLine(cmd.Context(), c, out)
				return nil
			}

			list, err := c.ListUPS(cmd.Context())
			if err != nil {
				return err
			}
			switch {
			case asJSON:
				return writeJSON(out, list)
			case len(list) == 0:
				cfg, _, _ := a.loadConfig()
				path := "the config file"
				if cfg != nil && cfg.Path != "" {
					path = cfg.Path
				}
				fmt.Fprintf(out, noUPSHelp, path)
			case len(list) == 1:
				writeDetail(out, a.painter(), list[0], time.Now())
				a.printShutdownLine(cmd.Context(), c, out)
			default:
				writeStatusTable(out, a.painter(), list)
				fmt.Fprintln(out)
				a.printShutdownLine(cmd.Context(), c, out)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func (a *app) varsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "vars <ups>",
		Short: "List every variable a UPS reports",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			u, err := c.GetUPS(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), u.Vars)
			}
			writeVars(cmd.OutOrStdout(), a.painter(), u)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func (a *app) cmdCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cmd <ups> [command] [param]",
		Short: "Run an instant command on a UPS, or list its commands",
		Example: `  currently cmd rack                       # list commands
  currently cmd rack test.battery.start
  currently cmd demo sim.power.fail         # rehearse a power failure`,
		Args: cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				u, err := c.GetUPS(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if len(u.Commands) == 0 {
					fmt.Fprintf(out, "UPS %s supports no commands.\n", u.Name)
					return nil
				}
				t := &table{p: a.painter()}
				t.addText("COMMAND", "DESCRIPTION")
				for _, c := range u.Commands {
					t.addText(c.Name, c.Description)
				}
				t.write(out)
				return nil
			}
			param := ""
			if len(args) == 3 {
				param = args[2]
			}
			if err := c.RunCommand(cmd.Context(), args[0], args[1], param); err != nil {
				return err
			}
			fmt.Fprintf(out, "Sent %s to %s.\n", args[1], args[0])
			return nil
		},
	}
}

func (a *app) setCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set <ups> <variable> <value>",
		Short:   "Change a writable UPS variable",
		Example: "  currently set rack battery.charge.low 30",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if err := c.SetVar(cmd.Context(), args[0], args[1], args[2]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Set %s on %s to %s.\n", args[1], args[0], args[2])
			return nil
		},
	}
}
