// Package version holds the nutng version, set at build time with
// -ldflags "-X github.com/nut-ng/nut-ng/internal/version.Version=v1.2.3".
package version

import "runtime/debug"

// Version of nutng.
var Version = "dev"

// String returns the version, falling back to the VCS revision for
// development builds.
func String() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return "dev-" + s.Value[:7]
			}
		}
	}
	return Version
}
