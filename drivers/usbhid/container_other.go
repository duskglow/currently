//go:build !linux

package usbhid

// Container returns "": container detection is only needed on Linux,
// where device nodes must be passed into containers.
func Container() string { return "" }

// NodeHelp says how to fix a missing device node; device nodes only go
// missing on Linux.
func NodeHelp() string { return "" }
