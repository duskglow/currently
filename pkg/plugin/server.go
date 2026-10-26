package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	currentlyv1 "github.com/duskglow/currently/gen/currently/v1"
	"github.com/duskglow/currently/pkg/sdk"
)

// driverServer serves an sdk.Driver over gRPC inside a plugin process.
type driverServer struct {
	currentlyv1.UnimplementedDriverServiceServer
	d   sdk.Driver
	log *slog.Logger

	mu      sync.Mutex
	next    int
	devices map[string]sdk.Device
}

func newDriverServer(d sdk.Driver, log *slog.Logger) *driverServer {
	return &driverServer{d: d, log: log, devices: map[string]sdk.Device{}}
}

func errorList(err error) []string {
	if err == nil {
		return nil
	}
	return strings.Split(err.Error(), "\n")
}

func (s *driverServer) DescribeDriver(context.Context, *currentlyv1.DescribeDriverRequest) (*currentlyv1.DescribeDriverResponse, error) {
	info := s.d.Info()
	info.Kind = sdk.KindDriver
	return &currentlyv1.DescribeDriverResponse{Info: InfoToProto(info)}, nil
}

func (s *driverServer) ValidateDriverConfig(_ context.Context, req *currentlyv1.ValidateDriverConfigRequest) (*currentlyv1.ValidateDriverConfigResponse, error) {
	return &currentlyv1.ValidateDriverConfigResponse{Errors: errorList(s.d.ValidateConfig(ConfigFromProto(req.GetConfig())))}, nil
}

func (s *driverServer) DiscoverDevices(ctx context.Context, _ *currentlyv1.DiscoverDevicesRequest) (*currentlyv1.DiscoverDevicesResponse, error) {
	found, err := s.d.Discover(ctx)
	if err != nil {
		return nil, ErrToStatus(err)
	}
	resp := &currentlyv1.DiscoverDevicesResponse{}
	for _, f := range found {
		cfg, err := ConfigToProto(f.Config)
		if err != nil {
			return nil, ErrToStatus(err)
		}
		desc := f.Description
		if f.Name != "" && desc == "" {
			desc = f.Name
		}
		resp.Devices = append(resp.Devices, &currentlyv1.DiscoveredDevice{Id: f.ID, Description: desc, Config: cfg})
	}
	return resp, nil
}

func (s *driverServer) OpenDevice(ctx context.Context, req *currentlyv1.OpenDeviceRequest) (*currentlyv1.OpenDeviceResponse, error) {
	log := s.log
	if req.GetUps() != "" {
		log = log.With("ups", req.GetUps())
	}
	dev, err := s.d.Open(ctx, ConfigFromProto(req.GetConfig()), log)
	if err != nil {
		return nil, ErrToStatus(err)
	}
	s.mu.Lock()
	s.next++
	id := strconv.Itoa(s.next)
	s.devices[id] = dev
	s.mu.Unlock()
	return &currentlyv1.OpenDeviceResponse{DeviceId: id, Commands: CommandsToProto(dev.Commands()), Writable: dev.Writable()}, nil
}

func (s *driverServer) device(id string) (sdk.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no open device %q", id)
	}
	return d, nil
}

func (s *driverServer) WatchDevice(req *currentlyv1.WatchDeviceRequest, stream currentlyv1.DriverService_WatchDeviceServer) error {
	dev, err := s.device(req.GetDeviceId())
	if err != nil {
		return err
	}
	ctx := stream.Context()
	var sendErr error
	var once sync.Once
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	err = dev.Watch(ctx, func(u sdk.Update) {
		if err := stream.Send(&currentlyv1.WatchDeviceResponse{Vars: u.Vars, Removed: u.Removed}); err != nil {
			once.Do(func() { sendErr = err; cancel() })
		}
	})
	if sendErr != nil || stream.Context().Err() != nil {
		return nil // Currently went away; the stream is over
	}
	if err == nil {
		err = fmt.Errorf("device stopped reporting")
	}
	return ErrToStatus(err)
}

func (s *driverServer) RunCommand(ctx context.Context, req *currentlyv1.RunCommandRequest) (*currentlyv1.RunCommandResponse, error) {
	dev, err := s.device(req.GetDeviceId())
	if err != nil {
		return nil, err
	}
	return &currentlyv1.RunCommandResponse{}, ErrToStatus(dev.RunCommand(ctx, req.GetCommand(), req.GetParam()))
}

func (s *driverServer) SetVar(ctx context.Context, req *currentlyv1.SetVarRequest) (*currentlyv1.SetVarResponse, error) {
	dev, err := s.device(req.GetDeviceId())
	if err != nil {
		return nil, err
	}
	return &currentlyv1.SetVarResponse{}, ErrToStatus(dev.SetVar(ctx, req.GetName(), req.GetValue()))
}

func (s *driverServer) CloseDevice(_ context.Context, req *currentlyv1.CloseDeviceRequest) (*currentlyv1.CloseDeviceResponse, error) {
	s.mu.Lock()
	dev, ok := s.devices[req.GetDeviceId()]
	delete(s.devices, req.GetDeviceId())
	s.mu.Unlock()
	if ok {
		return &currentlyv1.CloseDeviceResponse{}, ErrToStatus(dev.Close())
	}
	return &currentlyv1.CloseDeviceResponse{}, nil
}

// outputServer serves an sdk.Output over gRPC inside a plugin process.
type outputServer struct {
	currentlyv1.UnimplementedOutputServiceServer
	o   sdk.Output
	log *slog.Logger
}

func newOutputServer(o sdk.Output, log *slog.Logger) *outputServer {
	return &outputServer{o: o, log: log}
}

func (s *outputServer) DescribeOutput(context.Context, *currentlyv1.DescribeOutputRequest) (*currentlyv1.DescribeOutputResponse, error) {
	info := s.o.Info()
	info.Kind = sdk.KindOutput
	return &currentlyv1.DescribeOutputResponse{Info: InfoToProto(info)}, nil
}

func (s *outputServer) ValidateOutputConfig(_ context.Context, req *currentlyv1.ValidateOutputConfigRequest) (*currentlyv1.ValidateOutputConfigResponse, error) {
	return &currentlyv1.ValidateOutputConfigResponse{Errors: errorList(s.o.ValidateConfig(ConfigFromProto(req.GetConfig())))}, nil
}

func (s *outputServer) RunOutput(ctx context.Context, req *currentlyv1.RunOutputRequest) (*currentlyv1.RunOutputResponse, error) {
	host, err := DialHost(ctx, req.GetHostAddress(), req.GetHostToken())
	if err != nil {
		return nil, ErrToStatus(err)
	}
	defer host.Close()
	err = s.o.Run(ctx, host, ConfigFromProto(req.GetConfig()), s.log)
	if ctx.Err() != nil {
		return &currentlyv1.RunOutputResponse{}, nil // stopped by Currently
	}
	if err == nil {
		err = fmt.Errorf("output stopped")
	}
	return nil, ErrToStatus(err)
}
