package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/duskglow/currently/drivers/usbhid"
	"github.com/duskglow/currently/internal/version"
	"github.com/duskglow/currently/pkg/sdk"
)

func (a *app) debugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Tools for troubleshooting and supporting new UPS models",
	}
	cmd.AddCommand(a.captureCmd())
	return cmd
}

func (a *app) captureCmd() *cobra.Command {
	var (
		output     string
		samples    int
		interval   time.Duration
		keepSerial bool
		sel        usbhid.Options
	)
	cmd := &cobra.Command{
		Use:   "capture",
		Short: "Record a USB UPS's raw data to a file",
		Long: `Record every field a USB UPS reports, several times, to a JSON file.

Attach the file to a bug report when a UPS shows wrong or missing values,
or replay it without the hardware:

  ups:
    lab:
      driver: replay
      file: capture.json

The serial number is replaced with REDACTED unless --keep-serial is given.
currently serve may keep running while you capture.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sel.Interval = interval
			drv := usbhid.New()
			if err := drv.ValidateConfig(optionsConfig(sel)); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			c, err := drv.CaptureDevice(cmd.Context(), usbhid.CaptureOptions{
				Device: sel, Samples: samples, Interval: interval, KeepSerial: keepSerial,
				CurrentlyVersion: version.String(),
				Progress: func(n int, vars map[string]string) {
					fmt.Fprintf(out, "sample %d/%d: %s, charge %s%%, runtime %s, load %s%%\n", n, samples,
						orDash(sdk.ParseStatus(vars["ups.status"]).Describe()), orDash(vars["battery.charge"]),
						orDash(sdk.FormatSeconds(vars["battery.runtime"])), orDash(vars["ups.load"]))
				},
			})
			if err != nil && (c == nil || len(c.Samples) == 0) {
				return err
			}
			if output == "" {
				output = fmt.Sprintf("currently-capture-%04x-%04x-%s.json", c.Device.VendorID, c.Device.ProductID, time.Now().Format("20060102-150405"))
			}
			data, jerr := json.MarshalIndent(c, "", "  ")
			if jerr != nil {
				return jerr
			}
			if werr := os.WriteFile(output, append(data, '\n'), 0o644); werr != nil {
				return werr
			}
			fmt.Fprintf(out, "\nWrote %s: %s, %d fields, %d samples.\n", output, c.Device.Description(), len(c.Fields), len(c.Samples))
			if len(c.Unmapped) > 0 {
				fmt.Fprintf(out, "%d fields are not mapped to any variable yet (listed under \"unmapped\" in the file).\n", len(c.Unmapped))
			}
			return err
		},
	}
	f := cmd.Flags()
	f.StringVarP(&output, "output", "o", "", "file to write (default currently-capture-<vendor>-<product>-<time>.json)")
	f.IntVar(&samples, "samples", 5, "number of reads")
	f.DurationVar(&interval, "interval", 2*time.Second, "time between reads")
	f.BoolVar(&keepSerial, "keep-serial", false, "keep the serial number in the file")
	f.StringVar(&sel.ID, "id", "", `choose the UPS by "vvvv:pppp" vendor:product ID`)
	f.StringVar(&sel.Serial, "serial", "", "choose the UPS by serial number")
	return cmd
}

// optionsConfig converts device selection flags to driver options.
func optionsConfig(o usbhid.Options) sdk.Config {
	c := sdk.Config{}
	if o.ID != "" {
		c["id"] = o.ID
	}
	if o.Serial != "" {
		c["serial"] = o.Serial
	}
	return c
}
