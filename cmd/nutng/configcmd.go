package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nut-ng/nut-ng/internal/config"
	"github.com/nut-ng/nut-ng/internal/registry"
)

func (a *app) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Check or show the configuration",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Check the config file without starting nutng",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, found, err := a.loadConfig()
			if err != nil {
				fmt.Fprintln(a.stderr, err)
				return exitError(1)
			}
			if err := cfg.Validate(registry.Builtin()); err != nil {
				fmt.Fprintln(a.stderr, err)
				return exitError(1)
			}
			out := cmd.OutOrStdout()
			if !found {
				fmt.Fprintf(out, "No config file at %s; nutng will run with defaults.\n", config.DefaultPath())
				return nil
			}
			names := cfg.UPSNames()
			http := cfg.Listen.HTTP
			if http == "" {
				http = "off"
			}
			fmt.Fprintf(out, "OK: %s is valid.\n  UPS:      %d (%s)\n  REST API: %s\n",
				cfg.Path, len(names), strings.Join(names, ", "), http)
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
