//go:build !windows

package term

import "os"

// EnableANSI reports whether ANSI escape codes work on f; they always do
// outside Windows.
func EnableANSI(*os.File) bool { return true }
