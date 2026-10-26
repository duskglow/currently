package nutimport

import (
	"context"
	"errors"
	"log/slog"

	"github.com/duskglow/currently/internal/registry"
	"github.com/duskglow/currently/pkg/sdk"
)

// PluginDrivers are the driver plugins a converted config may use.
var PluginDrivers = []string{"snmp", "serial"}

// Registry returns the built-in drivers plus stand-ins for PluginDrivers,
// for checking a converted config. `currently config validate` checks the
// plugins' own options once they are installed.
func Registry() *registry.Registry {
	r := registry.Builtin()
	for _, name := range PluginDrivers {
		r.AddDriver(standIn(name))
	}
	return r
}

type standIn string

func (s standIn) Info() sdk.Info {
	return sdk.Info{Name: string(s), Kind: sdk.KindDriver, Description: "driver plugin " + string(s)}
}
func (standIn) Discover(context.Context) ([]sdk.Discovered, error) { return nil, nil }
func (standIn) ValidateConfig(sdk.Config) error                    { return nil }
func (s standIn) Open(context.Context, sdk.Config, *slog.Logger) (sdk.Device, error) {
	return nil, errors.New("the " + string(s) + " driver plugin is not installed")
}
