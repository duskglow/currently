// Command nutng monitors UPSes and serves their state to monitoring tools.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/nut-ng/nut-ng/internal/client"
	"github.com/nut-ng/nut-ng/internal/config"
	"github.com/nut-ng/nut-ng/internal/version"
)

// app holds the global flags.
type app struct {
	configPath string
	socket     string
	url        string
	token      string

	stdin          io.Reader
	stdout, stderr io.Writer
}

func main() {
	a := &app{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}
	root := a.rootCmd()
	if err := root.ExecuteContext(context.Background()); err != nil {
		var exit exitError
		if errors.As(err, &exit) {
			os.Exit(int(exit))
		}
		fmt.Fprintln(os.Stderr, "nutng:", err)
		os.Exit(1)
	}
}

// exitError ends the program with a status code and no message (the
// command already printed one).
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "nutng",
		Short: "Monitor UPSes and serve their state to monitoring tools",
		Long: `nutng monitors UPSes (uninterruptible power supplies) and serves their
state over a REST API, so monitoring tools can query it.

Get started with a USB UPS:
  nutng init                # find the UPS and write a config file
  nutng serve               # run the daemon
  nutng status              # in another terminal

Try it without hardware:
  nutng serve --demo        # a simulated UPS`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)

	f := root.PersistentFlags()
	f.StringVarP(&a.configPath, "config", "c", "", "config file (default "+config.DefaultPath()+")")
	f.StringVar(&a.socket, "socket", "", "control socket of the daemon (default: from the config file)")
	f.StringVar(&a.url, "url", "", "talk to a daemon over HTTP instead, e.g. http://ups-host:9300")
	f.StringVar(&a.token, "token", os.Getenv("NUTNG_TOKEN"), "API token for changes over HTTP (default $NUTNG_TOKEN)")

	root.AddCommand(
		a.serveCmd(),
		a.initCmd(),
		a.detectCmd(),
		a.statusCmd(),
		a.watchCmd(),
		a.varsCmd(),
		a.cmdCmd(),
		a.setCmd(),
		a.configCmd(),
		a.debugCmd(),
		a.versionCmd(),
	)
	return root
}

// loadConfig loads the config file. With no --config flag, a missing
// default file yields the default config. The second result reports
// whether a file was read.
func (a *app) loadConfig() (*config.Config, bool, error) {
	path := a.configPath
	if path == "" {
		path = config.DefaultPath()
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return config.Default(), false, nil
		}
	}
	c, err := config.Load(path)
	if err != nil {
		return nil, false, err
	}
	return c, true, nil
}

// client connects to the daemon chosen by the flags or the config file.
func (a *app) client() (*client.Client, error) {
	if a.url != "" {
		return client.NewURL(a.url, a.token)
	}
	if a.socket != "" {
		return client.NewSocket(a.socket), nil
	}
	if c, _, err := a.loadConfig(); err == nil && c.Listen.Control != "" {
		return client.NewSocket(c.Listen.Control), nil
	}
	candidates := config.ControlCandidates()
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return client.NewSocket(p), nil
		}
	}
	return client.NewSocket(candidates[len(candidates)-1]), nil
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the nutng version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "nutng %s (%s, %s/%s)\n", version.String(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}
