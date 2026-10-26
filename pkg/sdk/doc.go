// Package sdk defines the contracts between the nut-ng core and the plugins
// that feed it data (drivers) or publish it (outputs).
//
// Built-in drivers and outputs implement these interfaces directly and run
// inside the nutng process. External plugins implement the same contracts
// over gRPC (see proto/nutng/v1), and the core adapts them to these
// interfaces, so built-in and external plugins are treated alike.
//
// Variable names and status flags follow Network UPS Tools conventions
// (battery.charge, ups.status = "OL CHRG", ...), so existing NUT knowledge,
// dashboards and clients carry over.
package sdk

import "errors"

// ProtocolVersion is the version of the plugin contract. It changes only
// when the contract changes incompatibly, independent of the nutng release.
const ProtocolVersion = 1

// Errors returned by drivers and by the core. Wrap them with fmt.Errorf("%w")
// to add detail; callers match them with errors.Is.
var (
	// ErrNotFound means the named UPS, variable or command does not exist.
	ErrNotFound = errors.New("not found")
	// ErrNotSupported means the device does not support the operation.
	ErrNotSupported = errors.New("not supported by this device")
	// ErrNotConnected means the UPS is configured but its driver is not
	// currently connected to the device.
	ErrNotConnected = errors.New("not connected")
	// ErrInvalid means an argument (a variable value, a command parameter)
	// was rejected.
	ErrInvalid = errors.New("invalid value")
)

// Kind says whether a plugin is a driver or an output.
type Kind string

const (
	KindDriver Kind = "driver"
	KindOutput Kind = "output"
)

// Info describes a plugin.
type Info struct {
	Name        string `json:"name"`
	Kind        Kind   `json:"kind"`
	Version     string `json:"version"`
	Description string `json:"description"`
}
