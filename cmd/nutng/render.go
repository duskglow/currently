package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nut-ng/nut-ng/internal/term"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// painter adds ANSI colors when enabled.
type painter struct{ on bool }

func (p painter) paint(color, s string) string {
	if !p.on || color == "" || s == "" {
		return s
	}
	return color + s + term.Reset
}

// cell is a table cell; width is measured on text, without the color.
type cell struct{ text, color string }

type table struct {
	p    painter
	rows [][]cell
}

func (t *table) add(cells ...cell) { t.rows = append(t.rows, cells) }

func (t *table) addText(texts ...string) {
	cells := make([]cell, len(texts))
	for i, s := range texts {
		cells[i] = cell{text: s}
	}
	t.add(cells...)
}

// write prints the table; the first row is the header.
func (t *table) write(w io.Writer) {
	var widths []int
	for _, row := range t.rows {
		for i, c := range row {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], utf8.RuneCountInString(c.text))
		}
	}
	for r, row := range t.rows {
		var b strings.Builder
		for i, c := range row {
			color := c.color
			if r == 0 {
				color = term.Bold
			}
			b.WriteString(t.p.paint(color, c.text))
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c.text)+2))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

// statusCell describes a UPS's link and power state.
func statusCell(u sdk.UPS) cell {
	switch u.State {
	case sdk.StateConnecting:
		return cell{"connecting...", term.Dim}
	case sdk.StateStale:
		return cell{"STALE: " + truncate(u.LastError, 50), term.Red}
	case sdk.StateFailed:
		return cell{"ERROR: " + truncate(u.LastError, 50), term.Red}
	}
	st := u.Status()
	color := term.Green
	if st.Has(sdk.FlagOnBattery) {
		color = term.Yellow
	}
	for _, f := range []sdk.Flag{sdk.FlagLowBattery, sdk.FlagOff, sdk.FlagReplaceBattery, sdk.FlagOverload, sdk.FlagForcedShutdown} {
		if st.Has(f) {
			color = term.Red
		}
	}
	return cell{st.Describe(), color}
}

// withUnit renders a variable's value with its unit.
func withUnit(v sdk.Variable) string {
	switch {
	case v.Value == "":
		return ""
	case v.Unit == "":
		return v.Value
	case v.Unit == sdk.UnitPercent:
		return v.Value + "%"
	default:
		return v.Value + " " + v.Unit
	}
}

func runtimeText(u sdk.UPS) string {
	if r := u.Value("battery.runtime"); r != "" {
		return sdk.FormatSeconds(r)
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// writeStatusTable prints one line per UPS.
func writeStatusTable(w io.Writer, p painter, list []sdk.UPS) {
	t := &table{p: p}
	t.addText("UPS", "STATUS", "CHARGE", "RUNTIME", "LOAD", "INPUT", "MODEL")
	for _, u := range list {
		t.add(
			cell{text: u.Name, color: term.Bold},
			statusCell(u),
			cell{text: orDash(withUnit(u.Vars["battery.charge"]))},
			cell{text: orDash(runtimeText(u))},
			cell{text: orDash(withUnit(u.Vars["ups.load"]))},
			cell{text: orDash(withUnit(u.Vars["input.voltage"]))},
			cell{text: orDash(u.Model()), color: term.Dim},
		)
	}
	t.write(w)
}

// writeDetail prints a readable summary of one UPS.
func writeDetail(w io.Writer, p painter, u sdk.UPS, now time.Time) {
	title := p.paint(term.Bold, u.Name)
	if m := u.Model(); m != "" {
		title += "  " + m
	}
	title += "  " + p.paint(term.Dim, "("+u.Driver+" driver)")
	fmt.Fprintln(w, title)
	if u.Description != "" {
		fmt.Fprintln(w, "  "+u.Description)
	}

	line := func(label, value, color string) {
		if value != "" {
			fmt.Fprintf(w, "  %-11s %s\n", label, p.paint(color, value))
		}
	}
	join := func(parts ...string) string {
		var keep []string
		for _, s := range parts {
			if s != "" && s != " left" {
				keep = append(keep, s)
			}
		}
		return strings.Join(keep, ", ")
	}

	sc := statusCell(u)
	line("Status", sc.text, sc.color)
	if u.Restarts > 0 && u.State == sdk.StateConnected {
		line("Link", fmt.Sprintf("connected (driver restarted %d times)", u.Restarts), "")
	}
	line("Battery", join(withUnit(u.Vars["battery.charge"]), runtimeText(u)+" left", withUnit(u.Vars["battery.voltage"])), "")
	line("Input", join(withUnit(u.Vars["input.voltage"]), withUnit(u.Vars["input.frequency"])), "")
	line("Output", join(withUnit(u.Vars["output.voltage"]), withUnit(u.Vars["output.frequency"])), "")
	line("Load", join(withUnit(u.Vars["ups.load"]), withUnit(u.Vars["ups.realpower"])), "")
	line("Temperature", withUnit(u.Vars["ups.temperature"]), "")
	line("Self-test", u.Value("ups.test.result"), "")
	line("Updated", ago(u.LastUpdate, now), term.Dim)
}

// writeVars prints every variable of a UPS.
func writeVars(w io.Writer, p painter, u sdk.UPS) {
	names := make([]string, 0, len(u.Vars))
	for k := range u.Vars {
		names = append(names, k)
	}
	sort.Strings(names)
	t := &table{p: p}
	t.addText("VARIABLE", "VALUE", "")
	for _, k := range names {
		v := u.Vars[k]
		rw := ""
		if v.Writable {
			rw = "writable"
		}
		t.add(cell{text: k}, cell{text: withUnit(v)}, cell{text: rw, color: term.Dim})
	}
	t.write(w)
}

// eventLine renders a notable event on one line.
func eventLine(p painter, e sdk.Event) string {
	color := ""
	switch e.Type {
	case sdk.EventOnBattery, sdk.EventCommLost, sdk.EventDriverError:
		color = term.Yellow
	case sdk.EventLowBattery, sdk.EventReplaceBattery, sdk.EventOverload, sdk.EventForcedShutdown:
		color = term.Red
	case sdk.EventOnLine, sdk.EventCommRestored:
		color = term.Green
	}
	return fmt.Sprintf("%s  %s  %s  %s",
		p.paint(term.Dim, e.Time.Local().Format("15:04:05")),
		e.UPS,
		p.paint(color, strings.ReplaceAll(string(e.Type), "_", " ")),
		e.Message)
}

func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return t.Local().Format("2006-01-02 15:04:05")
	}
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-3]) + "..."
}
