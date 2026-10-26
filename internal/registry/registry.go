// Package registry keeps the drivers and outputs Currently knows about.
package registry

import (
	"fmt"
	"maps"
	"slices"

	"github.com/duskglow/currently/drivers/execdriver"
	"github.com/duskglow/currently/drivers/nutclient"
	"github.com/duskglow/currently/drivers/simulator"
	"github.com/duskglow/currently/drivers/usbhid"
	"github.com/duskglow/currently/pkg/sdk"
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
// Currently.
func Builtin() *Registry {
	r := New()
	r.AddDriver(usbhid.New())
	r.AddDriver(usbhid.NewReplay())
	r.AddDriver(nutclient.New())
	r.AddDriver(execdriver.New())
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

// HasDriver reports whether a driver or output named name exists.
func (r *Registry) HasDriver(name string) bool {
	_, d := r.drivers[name]
	_, o := r.outputs[name]
	return d || o
}

// HasOutput reports whether an output or driver named name exists.
func (r *Registry) HasOutput(name string) bool { return r.HasDriver(name) }

// OutputNames returns the registered output names, sorted.
func (r *Registry) OutputNames() []string { return slices.Sorted(maps.Keys(r.outputs)) }

// Driver looks up a driver by name.
func (r *Registry) Driver(name string) (sdk.Driver, bool) {
	d, ok := r.drivers[name]
	return d, ok
}

// Drivers returns the registered drivers, sorted by name.
func (r *Registry) Drivers() []sdk.Driver {
	var out []sdk.Driver
	for _, name := range r.DriverNames() {
		out = append(out, r.drivers[name])
	}
	return out
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
