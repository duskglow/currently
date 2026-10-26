package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	nutngv1 "github.com/nut-ng/nut-ng/gen/nutng/v1"
	"github.com/nut-ng/nut-ng/pkg/plugin"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// Driver adapts a driver plugin to sdk.Driver.
type Driver struct {
	p    *process
	info sdk.Info
}

var _ sdk.Driver = (*Driver)(nil)

// Info implements sdk.Driver.
func (d *Driver) Info() sdk.Info { return d.info }

// Discover implements sdk.Driver.
func (d *Driver) Discover(ctx context.Context) ([]sdk.Discovered, error) {
	c, _, err := d.p.clients()
	if err != nil {
		return nil, err
	}
	resp, err := c.DiscoverDevices(ctx, &nutngv1.DiscoverDevicesRequest{})
	if err != nil {
		return nil, plugin.StatusToErr(err)
	}
	var out []sdk.Discovered
	for _, x := range resp.GetDevices() {
		out = append(out, sdk.Discovered{Driver: d.info.Name, ID: x.GetId(), Name: x.GetDescription(),
			Description: x.GetDescription(), Config: plugin.ConfigFromProto(x.GetConfig())})
	}
	return out, nil
}

// ValidateConfig implements sdk.Driver.
func (d *Driver) ValidateConfig(cfg sdk.Config) error {
	c, _, err := d.p.clients()
	if err != nil {
		return err
	}
	pc, err := plugin.ConfigToProto(cfg)
	if err != nil {
		return err
	}
	resp, err := c.ValidateDriverConfig(context.Background(), &nutngv1.ValidateDriverConfigRequest{Config: pc})
	if err != nil {
		return plugin.StatusToErr(err)
	}
	if len(resp.GetErrors()) > 0 {
		return errors.New(strings.Join(resp.GetErrors(), "\n"))
	}
	return nil
}

// Open implements sdk.Driver. A plugin that has exited is restarted here,
// so the core's retry with backoff also supervises the plugin process.
func (d *Driver) Open(ctx context.Context, cfg sdk.Config, log *slog.Logger) (sdk.Device, error) {
	c, _, err := d.p.clients()
	if err != nil {
		return nil, err
	}
	pc, err := plugin.ConfigToProto(cfg)
	if err != nil {
		return nil, err
	}
	resp, err := c.OpenDevice(ctx, &nutngv1.OpenDeviceRequest{Config: pc})
	if err != nil {
		err = plugin.StatusToErr(err)
		d.p.recordError(err)
		return nil, err
	}
	return &device{c: c, id: resp.GetDeviceId(), commands: plugin.CommandsFromProto(resp.GetCommands()),
		writable: resp.GetWritable(), log: log}, nil
}

type device struct {
	c        nutngv1.DriverServiceClient
	id       string
	commands []sdk.Command
	writable []string
	log      *slog.Logger
	once     sync.Once
}

func (d *device) Watch(ctx context.Context, sink func(sdk.Update)) error {
	stream, err := d.c.WatchDevice(ctx, &nutngv1.WatchDeviceRequest{DeviceId: d.id})
	if err != nil {
		return plugin.StatusToErr(err)
	}
	for {
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == io.EOF {
				return errors.New("plugin ended the device stream")
			}
			return fmt.Errorf("plugin: %w", plugin.StatusToErr(err))
		}
		sink(sdk.Update{Vars: resp.GetVars(), Removed: resp.GetRemoved()})
	}
}

func (d *device) Commands() []sdk.Command { return d.commands }
func (d *device) Writable() []string      { return d.writable }

func (d *device) RunCommand(ctx context.Context, name, param string) error {
	_, err := d.c.RunCommand(ctx, &nutngv1.RunCommandRequest{DeviceId: d.id, Command: name, Param: param})
	return plugin.StatusToErr(err)
}

func (d *device) SetVar(ctx context.Context, name, value string) error {
	_, err := d.c.SetVar(ctx, &nutngv1.SetVarRequest{DeviceId: d.id, Name: name, Value: value})
	return plugin.StatusToErr(err)
}

func (d *device) Close() error {
	var err error
	d.once.Do(func() {
		_, err = d.c.CloseDevice(context.Background(), &nutngv1.CloseDeviceRequest{DeviceId: d.id})
	})
	return plugin.StatusToErr(err)
}

// Output adapts an output plugin to sdk.Output.
type Output struct {
	p    *process
	info sdk.Info
}

var _ sdk.Output = (*Output)(nil)

// Info implements sdk.Output.
func (o *Output) Info() sdk.Info { return o.info }

// ValidateConfig implements sdk.Output.
func (o *Output) ValidateConfig(cfg sdk.Config) error {
	_, c, err := o.p.clients()
	if err != nil {
		return err
	}
	pc, err := plugin.ConfigToProto(cfg)
	if err != nil {
		return err
	}
	resp, err := c.ValidateOutputConfig(context.Background(), &nutngv1.ValidateOutputConfigRequest{Config: pc})
	if err != nil {
		return plugin.StatusToErr(err)
	}
	if len(resp.GetErrors()) > 0 {
		return errors.New(strings.Join(resp.GetErrors(), "\n"))
	}
	return nil
}

// Run implements sdk.Output: it serves HostService for the plugin and runs
// it until ctx ends or the plugin fails.
func (o *Output) Run(ctx context.Context, host sdk.Host, cfg sdk.Config, log *slog.Logger) error {
	_, c, err := o.p.clients()
	if err != nil {
		return err
	}
	pc, err := plugin.ConfigToProto(cfg)
	if err != nil {
		return err
	}
	allow := !IsReadOnly(host)
	hs, err := startHostServer(host, allow, log)
	if err != nil {
		return err
	}
	defer hs.stop()
	_, err = c.RunOutput(ctx, &nutngv1.RunOutputRequest{Config: pc, HostAddress: hs.addr, HostToken: hs.token, AllowControl: allow})
	if ctx.Err() != nil {
		return nil
	}
	if err == nil {
		err = errors.New("output plugin stopped")
	}
	err = plugin.StatusToErr(err)
	o.p.recordError(err)
	return err
}
