package plugins

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	currentlyv1 "github.com/duskglow/currently/gen/currently/v1"
	"github.com/duskglow/currently/pkg/plugin"
	"github.com/duskglow/currently/pkg/sdk"
)

// ReadOnlyHost wraps a host so that commands, variable changes and FSD are
// refused. Outputs get it unless their config sets allow_control.
type ReadOnlyHost struct{ sdk.Host }

var errReadOnly = fmt.Errorf("this output may not control UPSes; set allow_control: true for it in currently.yaml: %w", sdk.ErrNotSupported)

// RunCommand implements sdk.Host.
func (ReadOnlyHost) RunCommand(context.Context, string, string, string) error { return errReadOnly }

// SetVar implements sdk.Host.
func (ReadOnlyHost) SetVar(context.Context, string, string, string) error { return errReadOnly }

// SetForcedShutdown implements sdk.Host.
func (ReadOnlyHost) SetForcedShutdown(context.Context, string) error { return errReadOnly }

// IsReadOnly reports whether host refuses control calls.
func IsReadOnly(host sdk.Host) bool {
	_, ok := host.(ReadOnlyHost)
	return ok
}

// hostServer serves sdk.Host to one output plugin on 127.0.0.1, guarded by
// a random token.
type hostServer struct {
	currentlyv1.UnimplementedHostServiceServer
	host  sdk.Host
	allow bool
	srv   *grpc.Server
	addr  string
	token string
}

func startHostServer(host sdk.Host, allow bool, log *slog.Logger) (*hostServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("serve the plugin host API: %w", err)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		ln.Close()
		return nil, err
	}
	hs := &hostServer{host: host, allow: allow, addr: ln.Addr().String(), token: hex.EncodeToString(b)}
	hs.srv = grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if err := hs.check(ctx); err != nil {
				return nil, err
			}
			return h(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			if err := hs.check(ss.Context()); err != nil {
				return err
			}
			return h(srv, ss)
		}),
	)
	currentlyv1.RegisterHostServiceServer(hs.srv, hs)
	go func() {
		if err := hs.srv.Serve(ln); err != nil {
			log.Debug("plugin host API stopped", "error", err)
		}
	}()
	return hs, nil
}

func (hs *hostServer) check(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	for _, v := range md.Get("authorization") {
		if tok, ok := strings.CutPrefix(v, "Bearer "); ok && subtle.ConstantTimeCompare([]byte(tok), []byte(hs.token)) == 1 {
			return nil
		}
	}
	return status.Error(codes.Unauthenticated, "missing or wrong token")
}

func (hs *hostServer) stop() { hs.srv.Stop() }

func (hs *hostServer) ListUPS(context.Context, *currentlyv1.ListUPSRequest) (*currentlyv1.ListUPSResponse, error) {
	resp := &currentlyv1.ListUPSResponse{}
	for _, u := range hs.host.ListUPS() {
		resp.Ups = append(resp.Ups, plugin.UPSToProto(u))
	}
	return resp, nil
}

func (hs *hostServer) GetUPS(_ context.Context, req *currentlyv1.GetUPSRequest) (*currentlyv1.GetUPSResponse, error) {
	u, ok := hs.host.GetUPS(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no UPS named %q", req.GetName())
	}
	return &currentlyv1.GetUPSResponse{Ups: plugin.UPSToProto(u)}, nil
}

func (hs *hostServer) GetHistory(_ context.Context, req *currentlyv1.GetHistoryRequest) (*currentlyv1.GetHistoryResponse, error) {
	samples, err := hs.host.History(req.GetUps(), req.GetVariable())
	if err != nil {
		return nil, plugin.ErrToStatus(err)
	}
	resp := &currentlyv1.GetHistoryResponse{}
	for _, s := range samples {
		resp.Samples = append(resp.Samples, &currentlyv1.Sample{Time: timestamppb.New(s.Time), Value: s.Value})
	}
	return resp, nil
}

func (hs *hostServer) GetRecentEvents(_ context.Context, req *currentlyv1.GetRecentEventsRequest) (*currentlyv1.GetRecentEventsResponse, error) {
	resp := &currentlyv1.GetRecentEventsResponse{}
	for _, e := range hs.host.RecentEvents(int(req.GetLimit())) {
		resp.Events = append(resp.Events, plugin.EventToProto(e))
	}
	return resp, nil
}

func (hs *hostServer) SubscribeEvents(req *currentlyv1.SubscribeEventsRequest, stream currentlyv1.HostService_SubscribeEventsServer) error {
	ctx := stream.Context()
	ch := hs.host.Subscribe(ctx, 256)
	var last uint64
	send := func(e sdk.Event) error {
		if req.GetUps() != "" && e.UPS != req.GetUps() {
			return nil
		}
		return stream.Send(&currentlyv1.SubscribeEventsResponse{Event: plugin.EventToProto(e)})
	}
	if n := req.GetRecent(); n > 0 {
		for _, e := range hs.host.RecentEvents(int(n)) {
			if err := send(e); err != nil {
				return err
			}
			last = e.Seq
		}
	}
	for e := range ch {
		if e.Seq <= last {
			continue
		}
		if err := send(e); err != nil {
			return err
		}
	}
	return nil
}

func (hs *hostServer) denied() error {
	return status.Error(codes.PermissionDenied, errReadOnly.Error())
}

func (hs *hostServer) RunUPSCommand(ctx context.Context, req *currentlyv1.RunUPSCommandRequest) (*currentlyv1.RunUPSCommandResponse, error) {
	if !hs.allow {
		return nil, hs.denied()
	}
	return &currentlyv1.RunUPSCommandResponse{}, plugin.ErrToStatus(hs.host.RunCommand(ctx, req.GetUps(), req.GetCommand(), req.GetParam()))
}

func (hs *hostServer) SetUPSVar(ctx context.Context, req *currentlyv1.SetUPSVarRequest) (*currentlyv1.SetUPSVarResponse, error) {
	if !hs.allow {
		return nil, hs.denied()
	}
	return &currentlyv1.SetUPSVarResponse{}, plugin.ErrToStatus(hs.host.SetVar(ctx, req.GetUps(), req.GetVariable(), req.GetValue()))
}

func (hs *hostServer) SetUPSForcedShutdown(ctx context.Context, req *currentlyv1.SetUPSForcedShutdownRequest) (*currentlyv1.SetUPSForcedShutdownResponse, error) {
	if !hs.allow {
		return nil, hs.denied()
	}
	return &currentlyv1.SetUPSForcedShutdownResponse{}, plugin.ErrToStatus(hs.host.SetForcedShutdown(ctx, req.GetUps()))
}
