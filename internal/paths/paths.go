// Package paths holds the default file locations of Currently on each platform.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
)

// Config is where Currently looks for its config file when no path is
// given and CURRENTLY_CONFIG is unset.
func Config() string {
	if p := os.Getenv("CURRENTLY_CONFIG"); p != "" {
		return p
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(programData(), "currently", "currently.yaml")
	case "darwin", "freebsd":
		return "/usr/local/etc/currently/currently.yaml"
	default:
		return "/etc/currently/currently.yaml"
	}
}

// PluginDirs lists the directories searched for plugin executables, in
// order: next to the Currently executable, the config directory's plugins.d,
// then the system plugin directory.
func PluginDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "plugins"))
	}
	dirs = append(dirs, filepath.Join(ConfigDir(), "plugins.d"))
	switch runtime.GOOS {
	case "windows":
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		dirs = append(dirs, filepath.Join(pf, "currently", "plugins"))
	case "linux":
		dirs = append(dirs, "/usr/local/lib/currently/plugins", "/usr/lib/currently/plugins")
	default:
		dirs = append(dirs, "/usr/local/lib/currently/plugins")
	}
	// Packages install Currently in its plugin directory's parent, so the
	// first entry can repeat a later one.
	var out []string
	for _, d := range dirs {
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// ConfigDir is the directory of the config file, which also holds
// user-supplied files such as quirks.d.
func ConfigDir() string { return filepath.Dir(Config()) }

// ControlCandidates lists the default control socket locations, in order.
// The daemon uses the first one it can create; the CLI uses the first one
// that exists. Root (or a Windows service) uses the system-wide path; an
// ordinary user running `currently serve` for testing gets a per-user path.
func ControlCandidates() []string {
	if runtime.GOOS == "windows" {
		return []string{filepath.Join(programData(), "currently", "currently.sock")}
	}
	system := "/run/currently/currently.sock"
	if runtime.GOOS == "darwin" || runtime.GOOS == "freebsd" {
		system = "/var/run/currently/currently.sock"
	}
	uid := os.Getuid()
	if uid == 0 {
		return []string{system}
	}
	user := filepath.Join(os.TempDir(), "currently-"+strconv.Itoa(uid), "currently.sock")
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		user = filepath.Join(dir, "currently", "currently.sock")
	}
	return []string{system, user}
}

func programData() string {
	if p := os.Getenv("ProgramData"); p != "" {
		return p
	}
	return `C:\ProgramData`
}
