package plugin

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	currentlyv1 "github.com/duskglow/currently/gen/currently/v1"
	"github.com/duskglow/currently/pkg/sdk"
)

// HostClient is the sdk.Host an output plugin receives: calls go back to
// Currently over gRPC.
type HostClient struct {
	conn *grpc.ClientConn
	c    currentlyv1.HostServiceClient
	log  *slog.Logger
}

var _ sdk.Host = (*HostClient)(nil)

type bearer string

func (b bearer) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}

// The connection is to 127.0.0.1 only, so plaintext is acceptable.
func (bearer) RequireTransportSecurity() bool { return false }

// DialHost connects to the HostService Currently serves for this plugin.
func DialHost(_ context.Context, addr, token string) (*HostClient, error) {
	if addr == "" {
		return nil, fmt.Errorf("the host did not say where its HostService is; upgrade Currently")
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearer(token)))
	if err != nil {
		return nil, err
	}
	return &HostClient{conn: conn, c: currentlyv1.NewHostServiceClient(conn), log: Logger()}, nil
}

// Close closes the connection.
func (h *HostClient) Close() error { return h.conn.Close() }

// ListUPS implements sdk.Host. Errors are logged and give an empty list.
func (h *HostClient) ListUPS() []sdk.UPS {
	resp, err := h.c.ListUPS(context.Background(), &currentlyv1.ListUPSRequest{})
	if err != nil {
		h.log.Warn("ListUPS failed", "error", err)
		return nil
	}
	out := make([]sdk.UPS, 0, len(resp.GetUps()))
	for _, u := range resp.GetUps() {
		out = append(out, UPSFromProto(u))
	}
	return out
}

// GetUPS implements sdk.Host.
func (h *HostClient) GetUPS(name string) (sdk.UPS, bool) {
	resp, err := h.c.GetUPS(context.Background(), &currentlyv1.GetUPSRequest{Name: name})
	if err != nil {
		return sdk.UPS{}, false
	}
	return UPSFromProto(resp.GetUps()), true
}

// History implements sdk.Host.
func (h *HostClient) History(ups, variable string) ([]sdk.Sample, error) {
	resp, err := h.c.GetHistory(context.Background(), &currentlyv1.GetHistoryRequest{Ups: ups, Variable: variable})
	if err != nil {
		return nil, StatusToErr(err)
	}
	out := make([]sdk.Sample, 0, len(resp.GetSamples()))
	for _, s := range resp.GetSamples() {
		out = append(out, sdk.Sample{Time: s.GetTime().AsTime(), Value: s.GetValue()})
	}
	return out, nil
}

// RecentEvents implements sdk.Host.
func (h *HostClient) RecentEvents(n int) []sdk.Event {
	resp, err := h.c.GetRecentEvents(context.Background(), &currentlyv1.GetRecentEventsRequest{Limit: uint32(max(n, 0))})
	if err != nil {
		h.log.Warn("GetRecentEvents failed", "error", err)
		return nil
	}
	out := make([]sdk.Event, 0, len(resp.GetEvents()))
	for _, e := range resp.GetEvents() {
		out = append(out, EventFromProto(e))
	}
	return out
}

// Subscribe implements sdk.Host. The channel closes when ctx ends or the
// connection to Currently is lost.
func (h *HostClient) Subscribe(ctx context.Context, buffer int) <-chan sdk.Event {
	ch := make(chan sdk.Event, max(buffer, 1))
	go func() {
		defer close(ch)
		stream, err := h.c.SubscribeEvents(ctx, &currentlyv1.SubscribeEventsRequest{})
		if err != nil {
			h.log.Warn("SubscribeEvents failed", "error", err)
			return
		}
		for {
			resp, err := stream.Recv()
			if err != nil {
				if err != io.EOF && ctx.Err() == nil {
					h.log.Warn("event stream from Currently ended", "error", err)
				}
				return
			}
			select {
			case ch <- EventFromProto(resp.GetEvent()):
			default: // drop rather than block, as the core does
			}
		}
	}()
	return ch
}

// RunCommand implements sdk.Host.
func (h *HostClient) RunCommand(ctx context.Context, ups, command, param string) error {
	_, err := h.c.RunUPSCommand(ctx, &currentlyv1.RunUPSCommandRequest{Ups: ups, Command: command, Param: param})
	return StatusToErr(err)
}

// SetVar implements sdk.Host.
func (h *HostClient) SetVar(ctx context.Context, ups, variable, value string) error {
	_, err := h.c.SetUPSVar(ctx, &currentlyv1.SetUPSVarRequest{Ups: ups, Variable: variable, Value: value})
	return StatusToErr(err)
}

// SetForcedShutdown implements sdk.Host.
func (h *HostClient) SetForcedShutdown(ctx context.Context, ups string) error {
	_, err := h.c.SetUPSForcedShutdown(ctx, &currentlyv1.SetUPSForcedShutdownRequest{Ups: ups})
	return StatusToErr(err)
}
