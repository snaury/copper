// Code generated by protoc-gen-go.
// source: copperd.proto
// DO NOT EDIT!

/*
Package protocol is a generated protocol buffer package.

It is generated from these files:
	copperd.proto

It has these top-level messages:
	Route
	Endpoint
	SubscribeRequest
	SubscribeResponse
	GetEndpointsRequest
	GetEndpointsResponse
	UnsubscribeRequest
	UnsubscribeResponse
	PublishRequest
	PublishResponse
	UnpublishRequest
	UnpublishResponse
	SetRouteRequest
	SetRouteResponse
	LookupRouteRequest
	LookupRouteResponse
*/
package protocol

import proto "github.com/golang/protobuf/proto"
import math "math"

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = math.Inf

type Command int32

const (
	Command_Subscribe    Command = 1
	Command_GetEndpoints Command = 2
	Command_Unsubscribe  Command = 3
	Command_Publish      Command = 4
	Command_Unpublish    Command = 5
	Command_LookupRoute  Command = 6
)

var Command_name = map[int32]string{
	1: "Subscribe",
	2: "GetEndpoints",
	3: "Unsubscribe",
	4: "Publish",
	5: "Unpublish",
	6: "LookupRoute",
}
var Command_value = map[string]int32{
	"Subscribe":    1,
	"GetEndpoints": 2,
	"Unsubscribe":  3,
	"Publish":      4,
	"Unpublish":    5,
	"LookupRoute":  6,
}

func (x Command) Enum() *Command {
	p := new(Command)
	*p = x
	return p
}
func (x Command) String() string {
	return proto.EnumName(Command_name, int32(x))
}
func (x *Command) UnmarshalJSON(data []byte) error {
	value, err := proto.UnmarshalJSONEnum(Command_value, data, "Command")
	if err != nil {
		return err
	}
	*x = Command(value)
	return nil
}

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

type SubscribeRequest struct {
	Options          []*SubscribeRequest_Option `protobuf:"bytes,1,rep,name=options" json:"options,omitempty"`
	XXX_unrecognized []byte                     `json:"-"`
}

func (m *SubscribeRequest) Reset()         { *m = SubscribeRequest{} }
func (m *SubscribeRequest) String() string { return proto.CompactTextString(m) }
func (*SubscribeRequest) ProtoMessage()    {}

func (m *SubscribeRequest) GetOptions() []*SubscribeRequest_Option {
	if m != nil {
		return m.Options
	}
	return nil
}

type SubscribeRequest_Option struct {
	Service          *string `protobuf:"bytes,1,req,name=service" json:"service,omitempty"`
	Distance         *uint32 `protobuf:"varint,2,opt,name=distance" json:"distance,omitempty"`
	MaxRetries       *uint32 `protobuf:"varint,3,opt,name=max_retries" json:"max_retries,omitempty"`
	XXX_unrecognized []byte  `json:"-"`
}

func (m *SubscribeRequest_Option) Reset()         { *m = SubscribeRequest_Option{} }
func (m *SubscribeRequest_Option) String() string { return proto.CompactTextString(m) }
func (*SubscribeRequest_Option) ProtoMessage()    {}

func (m *SubscribeRequest_Option) GetService() string {
	if m != nil && m.Service != nil {
		return *m.Service
	}
	return ""
}

func (m *SubscribeRequest_Option) GetDistance() uint32 {
	if m != nil && m.Distance != nil {
		return *m.Distance
	}
	return 0
}

func (m *SubscribeRequest_Option) GetMaxRetries() uint32 {
	if m != nil && m.MaxRetries != nil {
		return *m.MaxRetries
	}
	return 0
}

type SubscribeResponse struct {
	TargetId         *int64 `protobuf:"zigzag64,1,req,name=target_id" json:"target_id,omitempty"`
	XXX_unrecognized []byte `json:"-"`
}

func (m *SubscribeResponse) Reset()         { *m = SubscribeResponse{} }
func (m *SubscribeResponse) String() string { return proto.CompactTextString(m) }
func (*SubscribeResponse) ProtoMessage()    {}

func (m *SubscribeResponse) GetTargetId() int64 {
	if m != nil && m.TargetId != nil {
		return *m.TargetId
	}
	return 0
}

type GetEndpointsRequest struct {
	TargetId         *int64 `protobuf:"zigzag64,1,req,name=target_id" json:"target_id,omitempty"`
	StreamUpdates    *bool  `protobuf:"varint,2,opt,name=stream_updates" json:"stream_updates,omitempty"`
	XXX_unrecognized []byte `json:"-"`
}

func (m *GetEndpointsRequest) Reset()         { *m = GetEndpointsRequest{} }
func (m *GetEndpointsRequest) String() string { return proto.CompactTextString(m) }
func (*GetEndpointsRequest) ProtoMessage()    {}

func (m *GetEndpointsRequest) GetTargetId() int64 {
	if m != nil && m.TargetId != nil {
		return *m.TargetId
	}
	return 0
}

func (m *GetEndpointsRequest) GetStreamUpdates() bool {
	if m != nil && m.StreamUpdates != nil {
		return *m.StreamUpdates
	}
	return false
}

type GetEndpointsResponse struct {
	Type             *Endpoint_EventType `protobuf:"varint,1,req,name=type,enum=protocol.Endpoint_EventType" json:"type,omitempty"`
	Endpoints        []*Endpoint         `protobuf:"bytes,2,rep,name=endpoints" json:"endpoints,omitempty"`
	XXX_unrecognized []byte              `json:"-"`
}

func (m *GetEndpointsResponse) Reset()         { *m = GetEndpointsResponse{} }
func (m *GetEndpointsResponse) String() string { return proto.CompactTextString(m) }
func (*GetEndpointsResponse) ProtoMessage()    {}

func (m *GetEndpointsResponse) GetType() Endpoint_EventType {
	if m != nil && m.Type != nil {
		return *m.Type
	}
	return Endpoint_ADDED
}

func (m *GetEndpointsResponse) GetEndpoints() []*Endpoint {
	if m != nil {
		return m.Endpoints
	}
	return nil
}

type UnsubscribeRequest struct {
	TargetId         *int64 `protobuf:"zigzag64,1,req,name=target_id" json:"target_id,omitempty"`
	XXX_unrecognized []byte `json:"-"`
}

func (m *UnsubscribeRequest) Reset()         { *m = UnsubscribeRequest{} }
func (m *UnsubscribeRequest) String() string { return proto.CompactTextString(m) }
func (*UnsubscribeRequest) ProtoMessage()    {}

func (m *UnsubscribeRequest) GetTargetId() int64 {
	if m != nil && m.TargetId != nil {
		return *m.TargetId
	}
	return 0
}

type UnsubscribeResponse struct {
	XXX_unrecognized []byte `json:"-"`
}

func (m *UnsubscribeResponse) Reset()         { *m = UnsubscribeResponse{} }
func (m *UnsubscribeResponse) String() string { return proto.CompactTextString(m) }
func (*UnsubscribeResponse) ProtoMessage()    {}

type PublishRequest struct {
	Name             *string `protobuf:"bytes,1,req,name=name" json:"name,omitempty"`
	TargetId         *int64  `protobuf:"zigzag64,2,req,name=target_id" json:"target_id,omitempty"`
	Distance         *uint32 `protobuf:"varint,3,opt,name=distance" json:"distance,omitempty"`
	Concurrency      *uint32 `protobuf:"varint,4,opt,name=concurrency" json:"concurrency,omitempty"`
	XXX_unrecognized []byte  `json:"-"`
}

func (m *PublishRequest) Reset()         { *m = PublishRequest{} }
func (m *PublishRequest) String() string { return proto.CompactTextString(m) }
func (*PublishRequest) ProtoMessage()    {}

func (m *PublishRequest) GetName() string {
	if m != nil && m.Name != nil {
		return *m.Name
	}
	return ""
}

func (m *PublishRequest) GetTargetId() int64 {
	if m != nil && m.TargetId != nil {
		return *m.TargetId
	}
	return 0
}

func (m *PublishRequest) GetDistance() uint32 {
	if m != nil && m.Distance != nil {
		return *m.Distance
	}
	return 0
}

func (m *PublishRequest) GetConcurrency() uint32 {
	if m != nil && m.Concurrency != nil {
		return *m.Concurrency
	}
	return 0
}

type PublishResponse struct {
	XXX_unrecognized []byte `json:"-"`
}

func (m *PublishResponse) Reset()         { *m = PublishResponse{} }
func (m *PublishResponse) String() string { return proto.CompactTextString(m) }
func (*PublishResponse) ProtoMessage()    {}

type UnpublishRequest struct {
	Name             *string `protobuf:"bytes,1,req,name=name" json:"name,omitempty"`
	XXX_unrecognized []byte  `json:"-"`
}

func (m *UnpublishRequest) Reset()         { *m = UnpublishRequest{} }
func (m *UnpublishRequest) String() string { return proto.CompactTextString(m) }
func (*UnpublishRequest) ProtoMessage()    {}

func (m *UnpublishRequest) GetName() string {
	if m != nil && m.Name != nil {
		return *m.Name
	}
	return ""
}

type UnpublishResponse struct {
	XXX_unrecognized []byte `json:"-"`
}

func (m *UnpublishResponse) Reset()         { *m = UnpublishResponse{} }
func (m *UnpublishResponse) String() string { return proto.CompactTextString(m) }
func (*UnpublishResponse) ProtoMessage()    {}

type SetRouteRequest struct {
	Name             *string  `protobuf:"bytes,1,req,name=name" json:"name,omitempty"`
	Routes           []*Route `protobuf:"bytes,2,rep,name=routes" json:"routes,omitempty"`
	XXX_unrecognized []byte   `json:"-"`
}

func (m *SetRouteRequest) Reset()         { *m = SetRouteRequest{} }
func (m *SetRouteRequest) String() string { return proto.CompactTextString(m) }
func (*SetRouteRequest) ProtoMessage()    {}

func (m *SetRouteRequest) GetName() string {
	if m != nil && m.Name != nil {
		return *m.Name
	}
	return ""
}

func (m *SetRouteRequest) GetRoutes() []*Route {
	if m != nil {
		return m.Routes
	}
	return nil
}

type SetRouteResponse struct {
	XXX_unrecognized []byte `json:"-"`
}

func (m *SetRouteResponse) Reset()         { *m = SetRouteResponse{} }
func (m *SetRouteResponse) String() string { return proto.CompactTextString(m) }
func (*SetRouteResponse) ProtoMessage()    {}

type LookupRouteRequest struct {
	Name             *string `protobuf:"bytes,1,req,name=name" json:"name,omitempty"`
	XXX_unrecognized []byte  `json:"-"`
}

func (m *LookupRouteRequest) Reset()         { *m = LookupRouteRequest{} }
func (m *LookupRouteRequest) String() string { return proto.CompactTextString(m) }
func (*LookupRouteRequest) ProtoMessage()    {}

func (m *LookupRouteRequest) GetName() string {
	if m != nil && m.Name != nil {
		return *m.Name
	}
	return ""
}

type LookupRouteResponse struct {
	Routes           []*Route `protobuf:"bytes,1,rep,name=routes" json:"routes,omitempty"`
	XXX_unrecognized []byte   `json:"-"`
}

func (m *LookupRouteResponse) Reset()         { *m = LookupRouteResponse{} }
func (m *LookupRouteResponse) String() string { return proto.CompactTextString(m) }
func (*LookupRouteResponse) ProtoMessage()    {}

func (m *LookupRouteResponse) GetRoutes() []*Route {
	if m != nil {
		return m.Routes
	}
	return nil
}

func init() {
	proto.RegisterEnum("protocol.Command", Command_name, Command_value)
	proto.RegisterEnum("protocol.Endpoint_EventType", Endpoint_EventType_name, Endpoint_EventType_value)
}
