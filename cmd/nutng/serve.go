package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nut-ng/nut-ng/drivers/usbhid"
	"github.com/nut-ng/nut-ng/internal/config"
	"github.com/nut-ng/nut-ng/internal/daemon"
	"github.com/nut-ng/nut-ng/internal/svcmgr"
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
			if logLevel != "" {
				cfg.Log.Level = logLevel
			}
			if logFormat != "" {
				cfg.Log.Format = logFormat
			}
			asService := !svcmgr.Interactive()
			log := newLogger(cfg.Log, asService)
			reg, pm := loadRegistry(cmd.Context(), cfg, log.With("component", "plugins"))
			defer pm.Close()

			autodetected := 0
			if !found && len(cfg.UPS) == 0 && !demo {
				// Zero-config: monitor every USB UPS attached.
				list, _ := discover(cmd.Context(), reg, map[string]bool{})
				for _, f := range list {
					if f.Driver == usbhid.DriverName {
						cfg.AddUPS(f.UPS, config.UPS{Driver: f.Driver, Description: f.Name, Options: f.Config})
						autodetected++
					}
				}
			}
			if err := cfg.Validate(reg); err != nil {
				if asService {
					log.Error("the config has problems", "error", err)
				}
				fmt.Fprintln(a.stderr, err)
				return errors.New("the config has problems; fix them and try again (`nutng config validate` checks without starting)")
			}
			if !found {
				log.Info("no config file found; using defaults", "looked_for", config.DefaultPath())
			}
			if autodetected > 0 {
				log.Info("monitoring the USB UPSes found; run `nutng init` to save this as a config file", "ups", autodetected)
			}

			run := func(ctx context.Context) error {
				return daemon.Run(ctx, daemon.Options{
					Config:   cfg,
					Registry: reg,
					Plugins:  pm,
					Log:      log,
					Version:  version.String(),
				})
			}
			if asService {
				return svcmgr.Run(run, log)
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return run(ctx)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&demo, "demo", false, `add a simulated UPS named "demo"`)
	f.StringVar(&logLevel, "log-level", "", "override log.level (debug, info, warn, error)")
	f.StringVar(&logFormat, "log-format", "", "override log.format (text, json)")
	return cmd
}

// newLogger logs to stderr, or to the Windows Event Log when running as a
// Windows service (elsewhere the service manager captures stderr).
func newLogger(c config.Log, asService bool) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.Level))
	opts := &slog.HandlerOptions{Level: level}
	if asService && runtime.GOOS == "windows" {
		if h, err := svcmgr.NewEventLogHandler(opts); err == nil {
			return slog.New(h)
		}
	}
	if c.Format == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
