// Command currently-output-testoutput is an output plugin for the loader tests.
// It appends what it sees to the file named by the "file" option.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/duskglow/currently/pkg/plugin"
	"github.com/duskglow/currently/pkg/sdk"
)

type output struct{}

func (output) Info() sdk.Info {
	return sdk.Info{Name: "testoutput", Version: "0.1.0", Description: "Test output plugin"}
}

type opts struct {
	File string `yaml:"file"`
}

func (output) ValidateConfig(cfg sdk.Config) error {
	var o opts
	if err := cfg.Decode(&o); err != nil {
		return err
	}
	if o.File == "" {
		return errors.New("file is required")
	}
	return nil
}

func (output) Run(ctx context.Context, host sdk.Host, cfg sdk.Config, log *slog.Logger) error {
	var o opts
	if err := cfg.Decode(&o); err != nil {
		return err
	}
	f, err := os.OpenFile(o.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, u := range host.ListUPS() {
		fmt.Fprintf(f, "ups %s %s\n", u.Name, u.Value("ups.status"))
	}
	err = host.RunCommand(ctx, "rack", "beeper.disable", "")
	fmt.Fprintf(f, "command error: %v\n", err)
	log.Info("test output running")
	events := host.Subscribe(ctx, 64)
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-events:
			if !ok {
				return errors.New("event stream closed")
			}
			fmt.Fprintf(f, "event %s %s\n", e.Type, e.UPS)
		}
	}
}

func main() { plugin.ServeOutput(output{}) }
