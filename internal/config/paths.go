package config

import "github.com/nut-ng/nut-ng/internal/paths"

// DefaultPath is where nutng looks for its config file when no path is
// given and NUTNG_CONFIG is unset.
func DefaultPath() string { return paths.Config() }

// ControlCandidates lists the default control socket locations, in order.
// See paths.ControlCandidates.
func ControlCandidates() []string { return paths.ControlCandidates() }
