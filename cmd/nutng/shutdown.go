package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nut-ng/nut-ng/internal/daemon"
	"github.com/nut-ng/nut-ng/internal/policy"
	"github.com/nut-ng/nut-ng/internal/term"
)

func (a *app) testCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Check what nutng would do, without doing it",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "shutdown",
		Short: "Show the shutdown policy and every step a shutdown would take (shuts nothing down)",
		Long: `Show the shutdown policy, how each UPS looks to it right now, and every
step nutng would take if it shut this computer down. Nothing is shut down.

To rehearse a real outage safely, set ` + "`dry_run: true`" + ` under shutdown in
nutng.yaml (every step is logged, none is done), or try it with a
simulated UPS (` + "`nutng serve --demo`" + `), which never shuts the computer down.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			st, live, err := a.shutdownStatus(cmd)
			if err != nil {
				return err
			}
			if !live {
				fmt.Fprintln(out, "nutng is not running; showing the policy from the config file.")
				fmt.Fprintln(out)
			}
			writeShutdownReport(out, a.painter(), st)
			return nil
		},
	})
	return cmd
}

// shutdownStatus asks the daemon, or builds the status from the config
// file when the daemon is not running.
func (a *app) shutdownStatus(cmd *cobra.Command) (policy.Status, bool, error) {
	if c, err := a.client(); err == nil {
		if st, err := c.Shutdown(cmd.Context()); err == nil {
			return st, true, nil
		}
	}
	cfg, _, err := a.loadConfig()
	if err != nil {
		return policy.Status{}, false, err
	}
	return policy.New(daemon.PolicyConfig(cfg), policy.Options{}).Status(), false, nil
}

func writeShutdownReport(w io.Writer, p painter, st policy.Status) {
	if !st.Enabled {
		fmt.Fprintln(w, "No shutdown policy: nutng will not shut this computer down.")
		fmt.Fprintln(w, "To shut down when the UPS battery runs low, add this to nutng.yaml:")
		fmt.Fprintln(w, "\nshutdown:\n  when:\n    low_battery: true       # the UPS's own low-battery signal\n    # on_battery_for: 5m     # or after this long on battery")
		return
	}
	line, color := shutdownLine(st)
	fmt.Fprintf(w, "Shutdown policy: %s\n", p.paint(color, line))

	var supplies []string
	for _, s := range st.Supplies {
		role := "primary: attached here"
		if s.Remote {
			role = "secondary: read from a NUT server"
		}
		if s.Simulated {
			role += ", simulated"
		}
		supplies = append(supplies, fmt.Sprintf("%s (%s)", s.Name, role))
	}
	fmt.Fprintf(w, "  Powered by:  %s\n", strings.Join(supplies, ", "))
	fmt.Fprintf(w, "  Needs:       %d healthy UPS\n", st.MinSupplies)
	fmt.Fprintf(w, "  Triggers:    a UPS on battery is critical when %s\n", strings.Join(st.Triggers, ", or "))
	fmt.Fprintf(w, "               any UPS is critical when FSD is set, or contact is lost while it is on battery\n")
	for _, s := range st.Supplies {
		switch {
		case s.Critical:
			fmt.Fprintf(w, "  Now:         %s is critical: %s\n", s.Name, p.paint(term.Red, s.Reason))
		case s.ShutdownIn != nil:
			fmt.Fprintf(w, "  Now:         %s is on battery; shutdown in %s unless power returns\n", s.Name,
				p.paint(term.Yellow, (time.Duration(*s.ShutdownIn)*time.Second).String()))
		}
	}
	if st.Blocked != "" {
		fmt.Fprintf(w, "  %s the operating system shutdown command is blocked (%s)\n", p.paint(term.Yellow, "Note:"), st.Blocked)
	}

	if len(st.Steps) > 0 {
		fmt.Fprintf(w, "\nLast shutdown (%s):\n", st.Reason)
		for i, s := range st.Steps {
			fmt.Fprintf(w, "  %d. %s %s\n", i+1, s.Action, p.paint(term.Dim, "→ "+s.Result))
		}
	}
	fmt.Fprintln(w, "\nIf it triggers, nutng will:")
	for i, s := range st.Plan {
		fmt.Fprintf(w, "  %d. %s\n", i+1, s)
	}
	if st.DryRun {
		fmt.Fprintln(w, "\ndry_run is on: each step will be logged but not done.")
	}
	fmt.Fprintln(w, "\nNothing was shut down.")
}

// shutdownLine summarizes the policy on one line.
func shutdownLine(st policy.Status) (string, string) {
	suffix := ""
	if st.DryRun {
		suffix = " (dry run)"
	}
	switch st.State {
	case policy.StateDisabled, "":
		return "off (no shutdown section in nutng.yaml)", term.Dim
	case policy.StateShuttingDown:
		return "SHUTTING DOWN: " + st.Reason + suffix, term.Red
	case policy.StateDone:
		return "shut down: " + st.Reason + suffix, term.Red
	}
	for _, s := range st.Supplies {
		if s.ShutdownIn != nil {
			return fmt.Sprintf("%s on battery: shutdown in %s unless power returns%s", s.Name,
				time.Duration(*s.ShutdownIn)*time.Second, suffix), term.Yellow
		}
	}
	return "armed: a UPS on battery is critical when " + strings.Join(st.Triggers, ", or ") + suffix, term.Green
}
