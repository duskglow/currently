// Package plugin lets drivers and outputs run as separate executables, so
// they can be added to Currently without rebuilding it.
//
// A driver plugin is a program named currently-driver-<name> whose main
// function calls ServeDriver; an output plugin is named currently-output-<name>
// and calls ServeOutput. Put the executable in a plugin directory (see
// `currently plugin list`) and Currently starts it. The implementation is the same
// sdk.Driver or sdk.Output interface the built-in drivers and outputs use.
//
//	func main() { plugin.ServeDriver(&myDriver{}) }
//
// Plugins in other languages speak the same gRPC protocol (proto/currently/v1)
// and the hashicorp/go-plugin handshake; see the Python SDK made by
// `currently plugin new --lang python`.
package plugin

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	currentlyv1 "github.com/duskglow/currently/gen/currently/v1"
	"github.com/duskglow/currently/pkg/sdk"
)

// Handshake is shared by Currently and its plugins. A plugin started by hand
// (without the magic cookie in its environment) explains that it must be
// started by Currently.
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  sdk.ProtocolVersion,
	MagicCookieKey:   "CURRENTLY_PLUGIN",
	MagicCookieValue: "8c4a5d0e-currently-plugin-v1",
}

// Plugin set keys.
const (
	KeyDriver = "driver"
	KeyOutput = "output"
)

// DriverPlugin connects a driver over gRPC. Impl is set in the plugin
// process only.
type DriverPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	Impl sdk.Driver
}

// GRPCServer implements goplugin.GRPCPlugin.
func (p *DriverPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	currentlyv1.RegisterDriverServiceServer(s, newDriverServer(p.Impl, Logger()))
	return nil
}

// GRPCClient implements goplugin.GRPCPlugin.
func (p *DriverPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return currentlyv1.NewDriverServiceClient(c), nil
}

// OutputPlugin connects an output over gRPC. Impl is set in the plugin
// process only.
type OutputPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	Impl sdk.Output
}

// GRPCServer implements goplugin.GRPCPlugin.
func (p *OutputPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	currentlyv1.RegisterOutputServiceServer(s, newOutputServer(p.Impl, Logger()))
	return nil
}

// GRPCClient implements goplugin.GRPCPlugin.
func (p *OutputPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return currentlyv1.NewOutputServiceClient(c), nil
}

// Set returns the plugin set Currently uses to talk to plugins.
func Set() goplugin.PluginSet {
	return goplugin.PluginSet{KeyDriver: &DriverPlugin{}, KeyOutput: &OutputPlugin{}}
}

// ServeDriver runs a driver plugin until Currently stops it.
func ServeDriver(d sdk.Driver) {
	serve(goplugin.PluginSet{KeyDriver: &DriverPlugin{Impl: d}})
}

// ServeOutput runs an output plugin until Currently stops it.
func ServeOutput(o sdk.Output) {
	serve(goplugin.PluginSet{KeyOutput: &OutputPlugin{Impl: o}})
}

// ParentPIDEnv carries Currently's process ID to its plugins, which exit when
// Currently does (even if Currently is killed and cannot stop them).
const ParentPIDEnv = "CURRENTLY_PARENT_PID"

func serve(set goplugin.PluginSet) {
	if os.Getenv(Handshake.MagicCookieKey) != Handshake.MagicCookieValue {
		os.Stderr.WriteString("This is a Currently plugin. Put it in a Currently plugin directory (see `currently plugin list`); Currently starts it.\n")
		os.Exit(1)
	}
	if pid, err := strconv.Atoi(os.Getenv(ParentPIDEnv)); err == nil && pid > 0 {
		go func() {
			waitParent(pid)
			os.Exit(0)
		}()
	}
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: Handshake,
		Plugins:         set,
		GRPCServer:      goplugin.DefaultGRPCServer,
		Logger:          hclog.New(&hclog.LoggerOptions{Output: os.Stderr, JSONFormat: true, Level: hclog.Info}),
	})
}

// Logger returns a logger for plugin code. Its lines appear in Currently's log
// with their level and attributes.
func Logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		// go-plugin reads hclog's JSON format from plugin stderr.
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.TimeKey:
				// Currently stamps the line itself; go-plugin rejects lines
				// whose @timestamp is not in its exact layout.
				return slog.Attr{}
			case slog.MessageKey:
				a.Key = "@message"
			case slog.LevelKey:
				a.Key = "@level"
				a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
			}
			return a
		},
	}))
}
