package term

import (
	"os"

	"golang.org/x/sys/windows"
)

// EnableANSI turns on virtual terminal processing so ANSI escape codes work
// in the Windows console. It reports whether that succeeded.
func EnableANSI(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
