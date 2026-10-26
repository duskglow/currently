package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/duskglow/currently/internal/config"
)

func (a *app) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Check or show the configuration",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Check the config file without starting Currently",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, found, err := a.loadConfig()
			if err != nil {
				fmt.Fprintln(a.stderr, err)
				return exitError(1)
			}
			reg, pm := a.registry(cmd.Context(), cfg)
			defer pm.Close()
			if err := cfg.Validate(reg); err != nil {
				fmt.Fprintln(a.stderr, err)
				return exitError(1)
			}
			out := cmd.OutOrStdout()
			if !found {
				fmt.Fprintf(out, "No config file at %s; Currently will run with defaults.\n", config.DefaultPath())
				return nil
			}
			names := cfg.UPSNames()
			orOff := func(s string) string {
				if s == "" {
					return "off"
				}
				return s
			}
			metrics := cfg.Listen.Metrics
			switch {
			case strings.EqualFold(metrics, "off"):
			case metrics == "" && cfg.Listen.HTTP != "":
				metrics = cfg.Listen.HTTP + "/metrics"
			}
			fmt.Fprintf(out, "OK: %s is valid.\n  UPS:          %d (%s)\n  REST API:     %s\n  NUT protocol: %s (%d users)\n  Metrics:      %s\n",
				cfg.Path, len(names), strings.Join(names, ", "), orOff(cfg.Listen.HTTP),
				orOff(cfg.Listen.NUT), len(cfg.Users), orOff(metrics))
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print the effective config, with defaults filled in",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := a.loadConfig()
			if err != nil {
				return err
			}
			data, err := cfg.YAML()
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	})
	return cmd
}
