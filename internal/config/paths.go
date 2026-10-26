package config

import "github.com/duskglow/currently/internal/paths"

// DefaultPath is where Currently looks for its config file when no path is
// given and CURRENTLY_CONFIG is unset.
func DefaultPath() string { return paths.Config() }

// ControlCandidates lists the default control socket locations, in order.
// See paths.ControlCandidates.
func ControlCandidates() []string { return paths.ControlCandidates() }
