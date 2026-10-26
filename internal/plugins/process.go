// Package plugins finds, starts and supervises external driver and output
// plugins, and adapts them to sdk.Driver and sdk.Output.
package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	currentlyv1 "github.com/duskglow/currently/gen/currently/v1"
	"github.com/duskglow/currently/pkg/plugin"
	"github.com/duskglow/currently/pkg/sdk"
)

// startTimeout bounds how long a plugin may take to print its handshake.
const startTimeout = 20 * time.Second

// process is one plugin executable. It is restarted on demand after it
// exits.
type process struct {
	command []string
	source  string
	log     *slog.Logger
	name    atomic.Value // string: the file name until the plugin describes itself

	mu      sync.Mutex
	client  *goplugin.Client
	driver  currentlyv1.DriverServiceClient
	output  currentlyv1.OutputServiceClient
	starts  int
	lastErr string
	stopped bool
}

func newProcess(command []string, source string, name string, log *slog.Logger) *process {
	p := &process{command: command, source: source, log: log}
	p.name.Store(name)
	return p
}

func (p *process) pluginName() string { return p.name.Load().(string) }

// ensure starts the process if it is not running.
func (p *process) ensure() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return errors.New("plugin stopped")
	}
	if p.client != nil && !p.client.Exited() {
		return nil
	}
	if p.client != nil {
		p.log.Warn("plugin exited; restarting it")
		p.client.Kill()
	}
	cmd := exec.Command(p.command[0], p.command[1:]...)
	cmd.Env = append(os.Environ(), plugin.ParentPIDEnv+"="+strconv.Itoa(os.Getpid()))
	c := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  plugin.Handshake,
		Plugins:          plugin.Set(),
		Cmd:              cmd,
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		Logger:           newHCLog(p.log, p.pluginName),
		StartTimeout:     startTimeout,
	})
	rpc, err := c.Client()
	if err != nil {
		c.Kill()
		p.lastErr = err.Error()
		return fmt.Errorf("start plugin %s: %w", p.source, err)
	}
	d, err := rpc.Dispense(plugin.KeyDriver)
	if err != nil {
		c.Kill()
		return err
	}
	o, err := rpc.Dispense(plugin.KeyOutput)
	if err != nil {
		c.Kill()
		return err
	}
	p.client, p.driver, p.output = c, d.(currentlyv1.DriverServiceClient), o.(currentlyv1.OutputServiceClient)
	p.starts++
	p.lastErr = ""
	return nil
}

func (p *process) clients() (currentlyv1.DriverServiceClient, currentlyv1.OutputServiceClient, error) {
	if err := p.ensure(); err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.driver, p.output, nil
}

// describe asks the plugin what it is. Kind is found by trying the driver
// service first.
func (p *process) describe(ctx context.Context) (sdk.Info, error) {
	d, o, err := p.clients()
	if err != nil {
		return sdk.Info{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var info *currentlyv1.PluginInfo
	if resp, err := d.DescribeDriver(ctx, &currentlyv1.DescribeDriverRequest{}); err == nil {
		info = resp.GetInfo()
	} else if status.Code(err) != codes.Unimplemented {
		return sdk.Info{}, fmt.Errorf("describe plugin %s: %w", p.source, err)
	} else if resp, err := o.DescribeOutput(ctx, &currentlyv1.DescribeOutputRequest{}); err == nil {
		info = resp.GetInfo()
	} else {
		return sdk.Info{}, fmt.Errorf("describe plugin %s: %w", p.source, err)
	}
	if v := info.GetProtocolVersion(); v != sdk.ProtocolVersion {
		return sdk.Info{}, fmt.Errorf("plugin %s speaks plugin protocol %d, but this Currently speaks %d", p.source, v, sdk.ProtocolVersion)
	}
	if info.GetName() == "" {
		return sdk.Info{}, fmt.Errorf("plugin %s did not report a name", p.source)
	}
	return plugin.InfoFromProto(info), nil
}

func (p *process) recordError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastErr = err.Error()
}

// state reports whether the process runs, how often it started, and the
// last error.
func (p *process) state() (running bool, starts int, lastErr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.client != nil && !p.client.Exited(), p.starts, p.lastErr
}

// stop ends the process but lets it start again on demand.
func (p *process) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		p.client.Kill()
		p.client = nil
	}
}

func (p *process) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	if p.client != nil {
		p.client.Kill()
	}
}

func (p *process) String() string { return strings.Join(p.command, " ") }
