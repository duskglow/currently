// Package paths holds the default file locations of nutng on each platform.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// Config is where nutng looks for its config file when no path is
// given and NUTNG_CONFIG is unset.
func Config() string {
	if p := os.Getenv("NUTNG_CONFIG"); p != "" {
		return p
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(programData(), "nutng", "nutng.yaml")
	case "darwin", "freebsd":
		return "/usr/local/etc/nutng/nutng.yaml"
	default:
		return "/etc/nutng/nutng.yaml"
	}
}

// ConfigDir is the directory of the config file, which also holds
// user-supplied files such as quirks.d.
func ConfigDir() string { return filepath.Dir(Config()) }

// ControlCandidates lists the default control socket locations, in order.
// The daemon uses the first one it can create; the CLI uses the first one
// that exists. Root (or a Windows service) uses the system-wide path; an
// ordinary user running `nutng serve` for testing gets a per-user path.
func ControlCandidates() []string {
	if runtime.GOOS == "windows" {
		return []string{filepath.Join(programData(), "nutng", "nutng.sock")}
	}
	system := "/run/nutng/nutng.sock"
	if runtime.GOOS == "darwin" || runtime.GOOS == "freebsd" {
		system = "/var/run/nutng/nutng.sock"
	}
	uid := os.Getuid()
	if uid == 0 {
		return []string{system}
	}
	user := filepath.Join(os.TempDir(), "nutng-"+strconv.Itoa(uid), "nutng.sock")
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		user = filepath.Join(dir, "nutng", "nutng.sock")
	}
	return []string{system, user}
}

func programData() string {
	if p := os.Getenv("ProgramData"); p != "" {
		return p
	}
	return `C:\ProgramData`
}
