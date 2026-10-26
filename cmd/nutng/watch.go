package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nut-ng/nut-ng/internal/client"
	"github.com/nut-ng/nut-ng/internal/term"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

const watchRecent = 8

func (a *app) watchCmd() *cobra.Command {
	var plain, asJSON, all bool
	cmd := &cobra.Command{
		Use:   "watch [ups]",
		Short: "Show live UPS state and events",
		Long: `Show live UPS state and recent events, updating as they happen.

When the output is not a terminal (or with --plain), nutng prints one line
per event instead, which suits logs and scripts; --json prints events as
JSON lines.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ups := ""
			if len(args) == 1 {
				ups = args[0]
				if _, err := c.GetUPS(cmd.Context(), ups); err != nil {
					return err
				}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			if plain || asJSON || a.stdout != os.Stdout || !term.IsTerminal(os.Stdout) || !term.EnableANSI(os.Stdout) {
				return a.watchLines(ctx, c, ups, asJSON, all)
			}
			return a.watchScreen(ctx, c, ups)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&plain, "plain", false, "print one line per event instead of a live screen")
	f.BoolVar(&asJSON, "json", false, "print events as JSON lines")
	f.BoolVar(&all, "all", false, "with --plain or --json, include update events (every variable change)")
	return cmd
}

// streamEvents follows the event stream, reconnecting after errors, until
// ctx is done. reset is called before each (re)connection, which replays
// recent events. A failure on the first connection is returned.
func streamEvents(ctx context.Context, c *client.Client, ups string, recent int,
	reset func(), onEvent func(sdk.Event), onError func(error)) error {
	first := true
	for {
		if reset != nil {
			reset()
		}
		got := false
		err := c.Events(ctx, ups, recent, func(e sdk.Event) {
			got = true
			onEvent(e)
		})
		if ctx.Err() != nil {
			return nil
		}
		if first && !got && errors.Is(err, client.ErrNotRunning) {
			return err
		}
		first = false
		onError(err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

func (a *app) watchLines(ctx context.Context, c *client.Client, ups string, asJSON, all bool) error {
	p := a.painter()
	enc := json.NewEncoder(a.stdout)
	return streamEvents(ctx, c, ups, 0, nil,
		func(e sdk.Event) {
			if !all && !e.Type.Notable() {
				return
			}
			if asJSON {
				_ = enc.Encode(e)
				return
			}
			if e.Type == sdk.EventUpdate {
				parts := make([]string, 0, len(e.Changed))
				for k, v := range e.Changed {
					parts = append(parts, k+"="+v)
				}
				e.Message = strings.Join(parts, " ")
			}
			fmt.Fprintln(a.stdout, eventLine(p, e))
		},
		func(err error) { fmt.Fprintf(a.stderr, "nutng: %v; reconnecting...\n", err) })
}

func (a *app) watchScreen(ctx context.Context, c *client.Client, ups string) error {
	p := painter{on: term.UseColor(os.Stdout)}
	events := make(chan sdk.Event, 64)
	errs := make(chan string, 4)
	resets := make(chan struct{}, 4)

	streamErr := make(chan error, 1)
	go func() {
		streamErr <- streamEvents(ctx, c, ups, watchRecent,
			func() { resets <- struct{}{} },
			func(e sdk.Event) {
				if e.Type.Notable() {
					events <- e
				}
			},
			func(err error) { errs <- err.Error() })
	}()

	out := os.Stdout
	fmt.Fprint(out, term.AltScreenOn, term.HideCursor)
	defer fmt.Fprint(out, term.ShowCursor, term.AltScreenOff)

	var recent []sdk.Event
	var linkErr string
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		a.renderWatch(ctx, c, p, ups, recent, linkErr)
		select {
		case <-ctx.Done():
			return nil
		case err := <-streamErr:
			if err != nil {
				fmt.Fprint(out, term.ShowCursor, term.AltScreenOff)
				return err
			}
			return nil
		case <-resets:
			recent = nil
		case e := <-events:
			linkErr = ""
			recent = append(recent, e)
			if len(recent) > watchRecent {
				recent = recent[len(recent)-watchRecent:]
			}
		case s := <-errs:
			linkErr = s
		case <-tick.C:
		}
	}
}

func (a *app) renderWatch(ctx context.Context, c *client.Client, p painter, ups string, recent []sdk.Event, linkErr string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	now := time.Now()

	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n\n", p.paint(term.Bold, "nutng watch"),
		p.paint(term.Dim, now.Format("15:04:05")+"  ·  Ctrl+C to quit"))

	if ups != "" {
		u, err := c.GetUPS(ctx, ups)
		if err != nil {
			fmt.Fprintln(&b, p.paint(term.Red, err.Error()))
		} else {
			writeDetail(&b, p, u, now)
		}
	} else {
		list, err := c.ListUPS(ctx)
		switch {
		case err != nil:
			fmt.Fprintln(&b, p.paint(term.Red, err.Error()))
		case len(list) == 0:
			fmt.Fprintln(&b, "No UPS is configured.")
		case len(list) == 1:
			writeDetail(&b, p, list[0], now)
		default:
			writeStatusTable(&b, p, list)
		}
	}

	fmt.Fprintf(&b, "\n%s\n", p.paint(term.Bold, "Recent events"))
	if len(recent) == 0 {
		fmt.Fprintln(&b, p.paint(term.Dim, "  (none yet)"))
	}
	for i := len(recent) - 1; i >= 0; i-- {
		fmt.Fprintln(&b, "  "+eventLine(p, recent[i]))
	}
	if linkErr != "" {
		fmt.Fprintf(&b, "\n%s\n", p.paint(term.Yellow, "Event stream: "+linkErr+" (retrying)"))
	}

	// Redraw in place: overwrite each line and clear what is left, which
	// avoids the flicker of clearing the whole screen first.
	frame := strings.ReplaceAll(b.String(), "\n", term.ClearLine+"\n")
	fmt.Fprint(os.Stdout, term.Home+frame+term.ClearToEnd)
}
