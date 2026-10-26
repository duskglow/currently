// Package term handles terminal detection and ANSI colors.
package term

import (
	"os"
	"strings"

	"golang.org/x/term"
)

// IsTerminal reports whether f is an interactive terminal.
func IsTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// UseColor reports whether to color output written to f. It honors the
// NO_COLOR convention (https://no-color.org) and enables ANSI processing on
// Windows consoles.
func UseColor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" || strings.EqualFold(os.Getenv("TERM"), "dumb") || !IsTerminal(f) {
		return false
	}
	return EnableANSI(f)
}

// Color codes.
const (
	Reset  = "\x1b[0m"
	Bold   = "\x1b[1m"
	Dim    = "\x1b[2m"
	Red    = "\x1b[31m"
	Green  = "\x1b[32m"
	Yellow = "\x1b[33m"
	Cyan   = "\x1b[36m"
)

// Screen control sequences.
const (
	AltScreenOn  = "\x1b[?1049h"
	AltScreenOff = "\x1b[?1049l"
	HideCursor   = "\x1b[?25l"
	ShowCursor   = "\x1b[?25h"
	Home         = "\x1b[H"
	ClearToEnd   = "\x1b[J"
	ClearLine    = "\x1b[K"
)
