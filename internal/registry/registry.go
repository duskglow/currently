// Package registry keeps the drivers and outputs nutng knows about.
package registry

import (
	"fmt"
	"maps"
	"slices"

	"github.com/nut-ng/nut-ng/drivers/simulator"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// Registry maps plugin names to implementations.
type Registry struct {
	drivers map[string]sdk.Driver
	outputs map[string]sdk.Output
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{drivers: map[string]sdk.Driver{}, outputs: map[string]sdk.Output{}}
}

// Builtin returns a registry holding the drivers and outputs compiled into
// nutng.
func Builtin() *Registry {
	r := New()
	r.AddDriver(simulator.New())
	return r
}

// AddDriver registers a driver under its Info().Name.
func (r *Registry) AddDriver(d sdk.Driver) {
	name := d.Info().Name
	if _, dup := r.drivers[name]; dup {
		panic(fmt.Sprintf("driver %q registered twice", name))
	}
	r.drivers[name] = d
}

// Driver looks up a driver by name.
func (r *Registry) Driver(name string) (sdk.Driver, bool) {
	d, ok := r.drivers[name]
	return d, ok
}

// DriverNames returns the registered driver names, sorted.
func (r *Registry) DriverNames() []string { return slices.Sorted(maps.Keys(r.drivers)) }

// AddOutput registers an output under its Info().Name.
func (r *Registry) AddOutput(o sdk.Output) {
	name := o.Info().Name
	if _, dup := r.outputs[name]; dup {
		panic(fmt.Sprintf("output %q registered twice", name))
	}
	r.outputs[name] = o
}

// Output looks up an output by name.
func (r *Registry) Output(name string) (sdk.Output, bool) {
	o, ok := r.outputs[name]
	return o, ok
}
