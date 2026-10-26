package plugin

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	nutngv1 "github.com/nut-ng/nut-ng/gen/nutng/v1"
	"github.com/nut-ng/nut-ng/pkg/sdk"
)

// ConfigToProto converts plugin settings for the wire.
func ConfigToProto(c sdk.Config) (*structpb.Struct, error) {
	if c == nil {
		c = sdk.Config{}
	}
	s, err := structpb.NewStruct(map[string]any(c))
	if err != nil {
		return nil, fmt.Errorf("plugin settings: %w", err)
	}
	return s, nil
}

// ConfigFromProto converts plugin settings from the wire.
func ConfigFromProto(s *structpb.Struct) sdk.Config {
	if s == nil {
		return sdk.Config{}
	}
	return sdk.Config(s.AsMap())
}

// InfoToProto converts plugin information.
func InfoToProto(i sdk.Info) *nutngv1.PluginInfo {
	k := nutngv1.PluginKind_PLUGIN_KIND_DRIVER
	if i.Kind == sdk.KindOutput {
		k = nutngv1.PluginKind_PLUGIN_KIND_OUTPUT
	}
	return &nutngv1.PluginInfo{Name: i.Name, Kind: k, Version: i.Version, Description: i.Description,
		ProtocolVersion: sdk.ProtocolVersion}
}

// InfoFromProto converts plugin information.
func InfoFromProto(p *nutngv1.PluginInfo) sdk.Info {
	k := sdk.KindDriver
	if p.GetKind() == nutngv1.PluginKind_PLUGIN_KIND_OUTPUT {
		k = sdk.KindOutput
	}
	return sdk.Info{Name: p.GetName(), Kind: k, Version: p.GetVersion(), Description: p.GetDescription()}
}

// CommandsToProto converts commands.
func CommandsToProto(cmds []sdk.Command) []*nutngv1.Command {
	out := make([]*nutngv1.Command, len(cmds))
	for i, c := range cmds {
		out[i] = &nutngv1.Command{Name: c.Name, Description: c.Description}
	}
	return out
}

// CommandsFromProto converts commands.
func CommandsFromProto(cmds []*nutngv1.Command) []sdk.Command {
	out := make([]sdk.Command, len(cmds))
	for i, c := range cmds {
		out[i] = sdk.Command{Name: c.GetName(), Description: c.GetDescription()}
	}
	return out
}

var connStates = map[sdk.ConnState]nutngv1.ConnState{
	sdk.StateConnecting: nutngv1.ConnState_CONN_STATE_CONNECTING,
	sdk.StateConnected:  nutngv1.ConnState_CONN_STATE_CONNECTED,
	sdk.StateStale:      nutngv1.ConnState_CONN_STATE_STALE,
	sdk.StateFailed:     nutngv1.ConnState_CONN_STATE_FAILED,
}

// UPSToProto converts a UPS snapshot.
func UPSToProto(u sdk.UPS) *nutngv1.UPS {
	vars := make(map[string]*nutngv1.Variable, len(u.Vars))
	for k, v := range u.Vars {
		vars[k] = &nutngv1.Variable{Name: v.Name, Value: v.Value, Unit: v.Unit, Writable: v.Writable, Updated: ts(v.Updated)}
	}
	return &nutngv1.UPS{Name: u.Name, Description: u.Description, Driver: u.Driver, State: connStates[u.State],
		LastError: u.LastError, LastUpdate: ts(u.LastUpdate), Restarts: int32(u.Restarts), Vars: vars,
		Commands: CommandsToProto(u.Commands)}
}

// UPSFromProto converts a UPS snapshot.
func UPSFromProto(p *nutngv1.UPS) sdk.UPS {
	u := sdk.UPS{Name: p.GetName(), Description: p.GetDescription(), Driver: p.GetDriver(), State: sdk.StateConnecting,
		LastError: p.GetLastError(), Restarts: int(p.GetRestarts()), Vars: map[string]sdk.Variable{},
		Commands: CommandsFromProto(p.GetCommands())}
	for s, ps := range connStates {
		if ps == p.GetState() {
			u.State = s
		}
	}
	if p.GetLastUpdate() != nil {
		u.LastUpdate = p.GetLastUpdate().AsTime()
	}
	for k, v := range p.GetVars() {
		sv := sdk.Variable{Name: v.GetName(), Value: v.GetValue(), Unit: v.GetUnit(), Writable: v.GetWritable()}
		if v.GetUpdated() != nil {
			sv.Updated = v.GetUpdated().AsTime()
		}
		u.Vars[k] = sv
	}
	return u
}

// EventToProto converts an event.
func EventToProto(e sdk.Event) *nutngv1.Event {
	return &nutngv1.Event{Seq: e.Seq, Time: ts(e.Time), Ups: e.UPS, Type: string(e.Type), Message: e.Message, Changed: e.Changed}
}

// EventFromProto converts an event.
func EventFromProto(p *nutngv1.Event) sdk.Event {
	e := sdk.Event{Seq: p.GetSeq(), UPS: p.GetUps(), Type: sdk.EventType(p.GetType()), Message: p.GetMessage(), Changed: p.GetChanged()}
	if p.GetTime() != nil {
		e.Time = p.GetTime().AsTime()
	}
	return e
}

func ts(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// ErrToStatus turns an sdk error into a gRPC status for the wire.
func ErrToStatus(err error) error {
	if err == nil {
		return nil
	}
	code := codes.Unknown
	switch {
	case errors.Is(err, sdk.ErrNotFound):
		code = codes.NotFound
	case errors.Is(err, sdk.ErrNotSupported):
		code = codes.Unimplemented
	case errors.Is(err, sdk.ErrInvalid):
		code = codes.InvalidArgument
	case errors.Is(err, sdk.ErrNotConnected):
		code = codes.Unavailable
	}
	return status.Error(code, err.Error())
}

// StatusToErr turns a gRPC status from the wire back into an sdk error.
func StatusToErr(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	msg := st.Message()
	var base error
	switch st.Code() {
	case codes.NotFound:
		base = sdk.ErrNotFound
	case codes.Unimplemented:
		base = sdk.ErrNotSupported
	case codes.InvalidArgument:
		base = sdk.ErrInvalid
	case codes.Unavailable:
		base = sdk.ErrNotConnected
	case codes.PermissionDenied:
		return errors.New(msg)
	default:
		return errors.New(msg)
	}
	// The message usually ends with the base error's text already.
	return &wireError{msg: strings.TrimSuffix(strings.TrimSuffix(msg, base.Error()), ": "), base: base}
}

type wireError struct {
	msg  string
	base error
}

func (e *wireError) Error() string {
	if e.msg == "" {
		return e.base.Error()
	}
	return e.msg + ": " + e.base.Error()
}

func (e *wireError) Unwrap() error { return e.base }
