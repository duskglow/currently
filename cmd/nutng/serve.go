package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nut-ng/nut-ng/drivers/usbhid"
	"github.com/nut-ng/nut-ng/internal/config"
	"github.com/nut-ng/nut-ng/internal/daemon"
	"github.com/nut-ng/nut-ng/internal/registry"
	"github.com/nut-ng/nut-ng/internal/version"
)

func (a *app) serveCmd() *cobra.Command {
	var demo bool
	var logLevel, logFormat string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the nutng daemon in the foreground",
		Long: `Run the nutng daemon in the foreground. Service managers (systemd, the
Windows service manager, launchd) run this command.

Without a config file, nutng monitors every USB UPS it finds and serves
the REST API on 127.0.0.1:9300. Use --demo to add a simulated UPS whose
mains power fails for one minute every three minutes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, found, err := a.loadConfig()
			if err != nil {
				return err
			}
			if demo {
				cfg.AddDemo()
			}
			autodetected := 0
			if !found && len(cfg.UPS) == 0 && !demo {
				// Zero-config: monitor every USB UPS attached.
				list, _ := discover(cmd.Context(), registry.Builtin(), map[string]bool{})
				for _, f := range list {
					if f.Driver == usbhid.DriverName {
						cfg.AddUPS(f.UPS, config.UPS{Driver: f.Driver, Description: f.Name, Options: f.Config})
						autodetected++
					}
				}
			}
			if logLevel != "" {
				cfg.Log.Level = logLevel
			}
			if logFormat != "" {
				cfg.Log.Format = logFormat
			}
			if err := cfg.Validate(registry.Builtin()); err != nil {
				fmt.Fprintln(a.stderr, err)
				return errors.New("the config has problems; fix them and try again (`nutng config validate` checks without starting)")
			}
			log := newLogger(cfg.Log)
			if !found {
				log.Info("no config file found; using defaults", "looked_for", config.DefaultPath())
			}
			if autodetected > 0 {
				log.Info("monitoring the USB UPSes found; run `nutng init` to save this as a config file", "ups", autodetected)
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx, daemon.Options{
				Config:   cfg,
				Registry: registry.Builtin(),
				Log:      log,
				Version:  version.String(),
			})
		},
	}
	f := cmd.Flags()
	f.BoolVar(&demo, "demo", false, `add a simulated UPS named "demo"`)
	f.StringVar(&logLevel, "log-level", "", "override log.level (debug, info, warn, error)")
	f.StringVar(&logFormat, "log-format", "", "override log.format (text, json)")
	return cmd
}

func newLogger(c config.Log) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.Level))
	opts := &slog.HandlerOptions{Level: level}
	if c.Format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
