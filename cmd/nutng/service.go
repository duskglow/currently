package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kardianos/service"
	"github.com/spf13/cobra"

	"github.com/nut-ng/nut-ng/internal/registry"
	"github.com/nut-ng/nut-ng/internal/svcmgr"
)

func (a *app) serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install and control nutng as a system service",
		Long: `Install nutng as a system service that starts at boot and restarts if it
fails: systemd (or OpenRC/SysV) on Linux, launchd on macOS, rc.d on
FreeBSD, and the Service Control Manager on Windows.

Installing and controlling the service needs root (sudo) or an
Administrator terminal on Windows.`,
	}
	cmd.AddCommand(a.serviceInstallCmd())
	cmd.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := svcmgr.Controller(nil, "")
			if err != nil {
				return err
			}
			_ = s.Stop()
			if err := s.Uninstall(); err != nil {
				return svcmgr.Describe("remove", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Removed the nutng service.")
			return nil
		},
	})
	for _, action := range []struct{ name, short, done string }{
		{"start", "Start the service", "Started"},
		{"stop", "Stop the service", "Stopped"},
		{"restart", "Restart the service (after changing nutng.yaml)", "Restarted"},
	} {
		cmd.AddCommand(&cobra.Command{
			Use:   action.name,
			Short: action.short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				s, err := svcmgr.Controller(nil, "")
				if err != nil {
					return err
				}
				if err := service.Control(s, action.name); err != nil {
					if st, _ := svcmgr.Status(); st == svcmgr.StateNotInstalled {
						return errors.New("the nutng service is not installed; install it with `nutng service install`")
					}
					return svcmgr.Describe(action.name, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s the nutng service.\n", action.done)
				return nil
			},
		})
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show whether the service is installed and running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := svcmgr.Status()
			if err != nil {
				return svcmgr.Describe("check", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "nutng service: %s (%s)\n", st, svcmgr.Platform())
			if st != svcmgr.StateRunning {
				return exitError(3)
			}
			return nil
		},
	})
	return cmd
}

func (a *app) serviceInstallCmd() *cobra.Command {
	var user string
	var noStart bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install nutng as a service and start it",
		Long: `Install nutng as a service that runs ` + "`nutng serve`" + ` at boot, then start it.

The service runs this nutng executable from where it is now, so put it in
a permanent place first (for example /usr/local/bin, or
C:\Program Files\nutng on Windows). With --config, the service uses that
config file; otherwise the default location.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			// Refuse to install a service that would fail to start.
			cfg, found, err := a.loadConfig()
			if err != nil {
				return fmt.Errorf("%w\nFix the config before installing the service", err)
			}
			if err := cfg.Validate(registry.Builtin()); err != nil {
				return fmt.Errorf("%w\nFix the config before installing the service", err)
			}

			args := []string{"serve"}
			if a.configPath != "" {
				abs, err := filepath.Abs(a.configPath)
				if err != nil {
					return err
				}
				args = append(args, "--config", abs)
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			if exe, err = filepath.EvalSymlinks(exe); err != nil {
				return err
			}
			if why := svcmgr.RiskyLocation(exe); why != "" {
				fmt.Fprintf(a.stderr, "Warning: the service will run %s, but %s. If that file moves or is deleted, the service stops working.\n", exe, why)
			}

			s, err := svcmgr.Controller(args, user)
			if err != nil {
				return err
			}
			if err := s.Install(); err != nil {
				if strings.Contains(strings.ToLower(err.Error()), "already exists") {
					return errors.New("the nutng service is already installed. Use `nutng service restart` to pick up changes, or `nutng service uninstall` first to reinstall")
				}
				return svcmgr.Describe("install", err)
			}
			fmt.Fprintf(out, "Installed the nutng service (%s), running: %s %s\n", svcmgr.Platform(), exe, strings.Join(args, " "))
			if !found {
				fmt.Fprintln(out, "No config file yet: the service monitors every USB UPS it finds. Run `nutng init` to write one, then `nutng service restart`.")
			}
			if !noStart {
				if err := s.Start(); err != nil {
					return svcmgr.Describe("start", err)
				}
				fmt.Fprintln(out, "Started it. Check it with `nutng status`.")
			}
			fmt.Fprintf(out, "Logs: %s\n", svcmgr.LogHint())
			return nil
		},
	}
	cmd.Flags().StringVar(&user, "user", "", "run the service as this user (Linux/macOS; default root)")
	cmd.Flags().BoolVar(&noStart, "no-start", false, "install without starting")
	return cmd
}
