// Code generated by protoc-gen-go.
// source: copperd.proto
// DO NOT EDIT!

/*
Package protocol is a generated protocol buffer package.

It is generated from these files:
	copperd.proto

It has these top-level messages:
	Error
	Route
	Endpoint
	Subscribe
	GetEndpoints
	Unsubscribe
	Publish
	Unpublish
	SetRoute
	LookupRoute
	Command
*/
package protocol

import proto "github.com/golang/protobuf/proto"
import math "math"

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = math.Inf

type Endpoint_EventType int32

const (
	Endpoint_ADDED    Endpoint_EventType = 1
	Endpoint_REMOVED  Endpoint_EventType = 2
	Endpoint_REPLACED Endpoint_EventType = 3
)

var Endpoint_EventType_name = map[int32]string{
	1: "ADDED",
	2: "REMOVED",
	3: "REPLACED",
}
var Endpoint_EventType_value = map[string]int32{
	"ADDED":    1,
	"REMOVED":  2,
	"REPLACED": 3,
}

func (x Endpoint_EventType) Enum() *Endpoint_EventType {
	p := new(Endpoint_EventType)
	*p = x
	return p
}
func (x Endpoint_EventType) String() string {
	return proto.EnumName(Endpoint_EventType_name, int32(x))
}
func (x *Endpoint_EventType) UnmarshalJSON(data []byte) error {
	value, err := proto.UnmarshalJSONEnum(Endpoint_EventType_value, data, "Endpoint_EventType")
	if err != nil {
		return err
	}
	*x = Endpoint_EventType(value)
	return nil
}

type Error struct {
	Code             *int32  `protobuf:"zigzag32,1,req,name=code" json:"code,omitempty"`
	Message          *string `protobuf:"bytes,2,opt,name=message" json:"message,omitempty"`
	XXX_unrecognized []byte  `json:"-"`
}

func (m *Error) Reset()         { *m = Error{} }
func (m *Error) String() string { return proto.CompactTextString(m) }
func (*Error) ProtoMessage()    {}

func (m *Error) GetCode() int32 {
	if m != nil && m.Code != nil {
		return *m.Code
	}
	return 0
}

func (m *Error) GetMessage() string {
	if m != nil && m.Message != nil {
		return *m.Message
	}
	return ""
}

type Route struct {
	Service          *string `protobuf:"bytes,1,req,name=service" json:"service,omitempty"`
	Weight           *uint32 `protobuf:"varint,2,req,name=weight" json:"weight,omitempty"`
	Distance         *uint32 `protobuf:"varint,3,opt,name=distance" json:"distance,omitempty"`
	XXX_unrecognized []byte  `json:"-"`
}

func (m *Route) Reset()         { *m = Route{} }
func (m *Route) String() string { return proto.CompactTextString(m) }
func (*Route) ProtoMessage()    {}

func (m *Route) GetService() string {
	if m != nil && m.Service != nil {
		return *m.Service
	}
	return ""
}

func (m *Route) GetWeight() uint32 {
	if m != nil && m.Weight != nil {
		return *m.Weight
	}
	return 0
}

func (m *Route) GetDistance() uint32 {
	if m != nil && m.Distance != nil {
		return *m.Distance
	}
	return 0
}

type Endpoint struct {
	Address          *string `protobuf:"bytes,1,req,name=address" json:"address,omitempty"`
	Network          *string `protobuf:"bytes,2,opt,name=network" json:"network,omitempty"`
	XXX_unrecognized []byte  `json:"-"`
}

func (m *Endpoint) Reset()         { *m = Endpoint{} }
func (m *Endpoint) String() string { return proto.CompactTextString(m) }
func (*Endpoint) ProtoMessage()    {}

func (m *Endpoint) GetAddress() string {
	if m != nil && m.Address != nil {
		return *m.Address
	}
	return ""
}

func (m *Endpoint) GetNetwork() string {
	if m != nil && m.Network != nil {
		return *m.Network
	}
	return ""
}

type Subscribe struct {
	Options          []*Subscribe_Option `protobuf:"bytes,1,rep,name=options" json:"options,omitempty"`
	XXX_unrecognized []byte              `json:"-"`
}

func (m *Subscribe) Reset()         { *m = Subscribe{} }
func (m *Subscribe) String() string { return proto.CompactTextString(m) }
func (*Subscribe) ProtoMessage()    {}

func (m *Subscribe) GetOptions() []*Subscribe_Option {
	if m != nil {
		return m.Options
	}
	return nil
}

type Subscribe_Result struct {
	Error            *Error `protobuf:"bytes,1,opt,name=error" json:"error,omitempty"`
	TargetId         *int64 `protobuf:"zigzag64,2,opt,name=target_id" json:"target_id,omitempty"`
	XXX_unrecognized []byte `json:"-"`
}

func (m *Subscribe_Result) Reset()         { *m = Subscribe_Result{} }
func (m *Subscribe_Result) String() string { return proto.CompactTextString(m) }
func (*Subscribe_Result) ProtoMessage()    {}

func (m *Subscribe_Result) GetError() *Error {
	if m != nil {
		return m.Error
	}
	return nil
}

func (m *Subscribe_Result) GetTargetId() int64 {
	if m != nil && m.TargetId != nil {
		return *m.TargetId
	}
	return 0
}

type Subscribe_Option struct {
	Service          *string `protobuf:"bytes,1,req,name=service" json:"service,omitempty"`
	Distance         *uint32 `protobuf:"varint,2,opt,name=distance" json:"distance,omitempty"`
	MaxRetries       *uint32 `protobuf:"varint,3,opt,name=max_retries" json:"max_retries,omitempty"`
	XXX_unrecognized []byte  `json:"-"`
}

func (m *Subscribe_Option) Reset()         { *m = Subscribe_Option{} }
func (m *Subscribe_Option) String() string { return proto.CompactTextString(m) }
func (*Subscribe_Option) ProtoMessage()    {}

func (m *Subscribe_Option) GetService() string {
	if m != nil && m.Service != nil {
		return *m.Service
	}
	return ""
}

func (m *Subscribe_Option) GetDistance() uint32 {
	if m != nil && m.Distance != nil {
		return *m.Distance
	}
	return 0
}

func (m *Subscribe_Option) GetMaxRetries() uint32 {
	if m != nil && m.MaxRetries != nil {
		return *m.MaxRetries
	}
	return 0
}

type GetEndpoints struct {
	TargetId         *int64 `protobuf:"zigzag64,1,req,name=target_id" json:"target_id,omitempty"`
	StreamEvents     *bool  `protobuf:"varint,2,opt,name=stream_events" json:"stream_events,omitempty"`
	XXX_unrecognized []byte `json:"-"`
}

func (m *GetEndpoints) Reset()         { *m = GetEndpoints{} }
func (m *GetEndpoints) String() string { return proto.CompactTextString(m) }
func (*GetEndpoints) ProtoMessage()    {}

func (m *GetEndpoints) GetTargetId() int64 {
	if m != nil && m.TargetId != nil {
		return *m.TargetId
	}
	return 0
}

func (m *GetEndpoints) GetStreamEvents() bool {
	if m != nil && m.StreamEvents != nil {
		return *m.StreamEvents
	}
	return false
}

type GetEndpoints_Result struct {
	Error            *Error      `protobuf:"bytes,1,opt,name=error" json:"error,omitempty"`
	Endpoints        []*Endpoint `protobuf:"bytes,2,rep,name=endpoints" json:"endpoints,omitempty"`
	XXX_unrecognized []byte      `json:"-"`
}

func (m *GetEndpoints_Result) Reset()         { *m = GetEndpoints_Result{} }
func (m *GetEndpoints_Result) String() string { return proto.CompactTextString(m) }
func (*GetEndpoints_Result) ProtoMessage()    {}

func (m *GetEndpoints_Result) GetError() *Error {
	if m != nil {
		return m.Error
	}
	return nil
}

func (m *GetEndpoints_Result) GetEndpoints() []*Endpoint {
	if m != nil {
		return m.Endpoints
	}
	return nil
}

type GetEndpoints_Event struct {
	Error            *Error              `protobuf:"bytes,1,opt,name=error" json:"error,omitempty"`
	Type             *Endpoint_EventType `protobuf:"varint,2,opt,name=type,enum=protocol.Endpoint_EventType" json:"type,omitempty"`
	Endpoints        []*Endpoint         `protobuf:"bytes,3,rep,name=endpoints" json:"endpoints,omitempty"`
	XXX_unrecognized []byte              `json:"-"`
}

func (m *GetEndpoints_Event) Reset()         { *m = GetEndpoints_Event{} }
func (m *GetEndpoints_Event) String() string { return proto.CompactTextString(m) }
func (*GetEndpoints_Event) ProtoMessage()    {}

func (m *GetEndpoints_Event) GetError() *Error {
	if m != nil {
		return m.Error
	}
	return nil
}

func (m *GetEndpoints_Event) GetType() Endpoint_EventType {
	if m != nil && m.Type != nil {
		return *m.Type
	}
	return Endpoint_ADDED
}

func (m *GetEndpoints_Event) GetEndpoints() []*Endpoint {
	if m != nil {
		return m.Endpoints
	}
	return nil
}

type Unsubscribe struct {
	TargetId         *int64 `protobuf:"zigzag64,1,req,name=target_id" json:"target_id,omitempty"`
	XXX_unrecognized []byte `json:"-"`
}

func (m *Unsubscribe) Reset()         { *m = Unsubscribe{} }
func (m *Unsubscribe) String() string { return proto.CompactTextString(m) }
func (*Unsubscribe) ProtoMessage()    {}

func (m *Unsubscribe) GetTargetId() int64 {
	if m != nil && m.TargetId != nil {
		return *m.TargetId
	}
	return 0
}

type Unsubscribe_Result struct {
	Error            *Error `protobuf:"bytes,1,opt,name=error" json:"error,omitempty"`
	XXX_unrecognized []byte `json:"-"`
}

func (m *Unsubscribe_Result) Reset()         { *m = Unsubscribe_Result{} }
func (m *Unsubscribe_Result) String() string { return proto.CompactTextString(m) }
func (*Unsubscribe_Result) ProtoMessage()    {}

func (m *Unsubscribe_Result) GetError() *Error {
	if m != nil {
		return m.Error
	}
	return nil
}

type Publish struct {
	Name             *string `protobuf:"bytes,1,req,name=name" json:"name,omitempty"`
	TargetId         *int64  `protobuf:"zigzag64,2,req,name=target_id" json:"target_id,omitempty"`
	Distance         *uint32 `protobuf:"varint,3,opt,name=distance" json:"distance,omitempty"`
	Concurrency      *uint32 `protobuf:"varint,4,opt,name=concurrency" json:"concurrency,omitempty"`
	XXX_unrecognized []byte  `json:"-"`
}

func (m *Publish) Reset()         { *m = Publish{} }
func (m *Publish) String() string { return proto.CompactTextString(m) }
func (*Publish) ProtoMessage()    {}

func (m *Publish) GetName() string {
	if m != nil && m.Name != nil {
		return *m.Name
	}
	return ""
}

func (m *Publish) GetTargetId() int64 {
	if m != nil && m.TargetId != nil {
		return *m.TargetId
	}
	return 0
}

func (m *Publish) GetDistance() uint32 {
	if m != nil && m.Distance != nil {
		return *m.Distance
	}
	return 0
}

func (m *Publish) GetConcurrency() uint32 {
	if m != nil && m.Concurrency != nil {
		return *m.Concurrency
	}
	return 0
}

type Publish_Result struct {
	Error            *Error `protobuf:"bytes,1,opt,name=error" json:"error,omitempty"`
	XXX_unrecognized []byte `json:"-"`
}

func (m *Publish_Result) Reset()         { *m = Publish_Result{} }
func (m *Publish_Result) String() string { return proto.CompactTextString(m) }
func (*Publish_Result) ProtoMessage()    {}

func (m *Publish_Result) GetError() *Error {
	if m != nil {
		return m.Error
	}
	return nil
}

type Unpublish struct {
	Name             *string `protobuf:"bytes,1,req,name=name" json:"name,omitempty"`
	XXX_unrecognized []byte  `json:"-"`
}

func (m *Unpublish) Reset()         { *m = Unpublish{} }
func (m *Unpublish) String() string { return proto.CompactTextString(m) }
func (*Unpublish) ProtoMessage()    {}

func (m *Unpublish) GetName() string {
	if m != nil && m.Name != nil {
		return *m.Name
	}
	return ""
}

type Unpublish_Result struct {
	Error            *Error `protobuf:"bytes,1,opt,name=error" json:"error,omitempty"`
	XXX_unrecognized []byte `json:"-"`
}

func (m *Unpublish_Result) Reset()         { *m = Unpublish_Result{} }
func (m *Unpublish_Result) String() string { return proto.CompactTextString(m) }
func (*Unpublish_Result) ProtoMessage()    {}

func (m *Unpublish_Result) GetError() *Error {
	if m != nil {
		return m.Error
	}
	return nil
}

type SetRoute struct {
	Name             *string  `protobuf:"bytes,1,req,name=name" json:"name,omitempty"`
	Routes           []*Route `protobuf:"bytes,2,rep,name=routes" json:"routes,omitempty"`
	XXX_unrecognized []byte   `json:"-"`
}

func (m *SetRoute) Reset()         { *m = SetRoute{} }
func (m *SetRoute) String() string { return proto.CompactTextString(m) }
func (*SetRoute) ProtoMessage()    {}

func (m *SetRoute) GetName() string {
	if m != nil && m.Name != nil {
		return *m.Name
	}
	return ""
}

func (m *SetRoute) GetRoutes() []*Route {
	if m != nil {
		return m.Routes
	}
	return nil
}

type SetRoute_Result struct {
	Error            *Error `protobuf:"bytes,1,opt,name=error" json:"error,omitempty"`
	XXX_unrecognized []byte `json:"-"`
}

func (m *SetRoute_Result) Reset()         { *m = SetRoute_Result{} }
func (m *SetRoute_Result) String() string { return proto.CompactTextString(m) }
func (*SetRoute_Result) ProtoMessage()    {}

func (m *SetRoute_Result) GetError() *Error {
	if m != nil {
		return m.Error
	}
	return nil
}

type LookupRoute struct {
	Name             *string `protobuf:"bytes,1,req,name=name" json:"name,omitempty"`
	XXX_unrecognized []byte  `json:"-"`
}

func (m *LookupRoute) Reset()         { *m = LookupRoute{} }
func (m *LookupRoute) String() string { return proto.CompactTextString(m) }
func (*LookupRoute) ProtoMessage()    {}

func (m *LookupRoute) GetName() string {
	if m != nil && m.Name != nil {
		return *m.Name
	}
	return ""
}

type LookupRoute_Result struct {
	Error            *Error   `protobuf:"bytes,1,opt,name=error" json:"error,omitempty"`
	Routes           []*Route `protobuf:"bytes,2,rep,name=routes" json:"routes,omitempty"`
	XXX_unrecognized []byte   `json:"-"`
}

func (m *LookupRoute_Result) Reset()         { *m = LookupRoute_Result{} }
func (m *LookupRoute_Result) String() string { return proto.CompactTextString(m) }
func (*LookupRoute_Result) ProtoMessage()    {}

func (m *LookupRoute_Result) GetError() *Error {
	if m != nil {
		return m.Error
	}
	return nil
}

func (m *LookupRoute_Result) GetRoutes() []*Route {
	if m != nil {
		return m.Routes
	}
	return nil
}

type Command struct {
	Subscribe        *Subscribe    `protobuf:"bytes,1,opt,name=subscribe" json:"subscribe,omitempty"`
	GetEndpoints     *GetEndpoints `protobuf:"bytes,2,opt,name=get_endpoints" json:"get_endpoints,omitempty"`
	Unsubscribe      *Unsubscribe  `protobuf:"bytes,3,opt,name=unsubscribe" json:"unsubscribe,omitempty"`
	Publish          *Publish      `protobuf:"bytes,4,opt,name=publish" json:"publish,omitempty"`
	Unpublish        *Unpublish    `protobuf:"bytes,5,opt,name=unpublish" json:"unpublish,omitempty"`
	SetRoute         *SetRoute     `protobuf:"bytes,6,opt,name=set_route" json:"set_route,omitempty"`
	LookupRoute      *LookupRoute  `protobuf:"bytes,7,opt,name=lookup_route" json:"lookup_route,omitempty"`
	XXX_unrecognized []byte        `json:"-"`
}

func (m *Command) Reset()         { *m = Command{} }
func (m *Command) String() string { return proto.CompactTextString(m) }
func (*Command) ProtoMessage()    {}

func (m *Command) GetSubscribe() *Subscribe {
	if m != nil {
		return m.Subscribe
	}
	return nil
}

func (m *Command) GetGetEndpoints() *GetEndpoints {
	if m != nil {
		return m.GetEndpoints
	}
	return nil
}

func (m *Command) GetUnsubscribe() *Unsubscribe {
	if m != nil {
		return m.Unsubscribe
	}
	return nil
}

func (m *Command) GetPublish() *Publish {
	if m != nil {
		return m.Publish
	}
	return nil
}

func (m *Command) GetUnpublish() *Unpublish {
	if m != nil {
		return m.Unpublish
	}
	return nil
}

func (m *Command) GetSetRoute() *SetRoute {
	if m != nil {
		return m.SetRoute
	}
	return nil
}

func (m *Command) GetLookupRoute() *LookupRoute {
	if m != nil {
		return m.LookupRoute
	}
	return nil
}

func init() {
	proto.RegisterEnum("protocol.Endpoint_EventType", Endpoint_EventType_name, Endpoint_EventType_value)
}
