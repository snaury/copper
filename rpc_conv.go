package copper

import (
	"github.com/golang/protobuf/proto"
	"github.com/snaury/copper/protocol"
)

func rpcProtoToSubscribeOptions(poptions []*protocol.SubscribeOption) []SubscribeOption {
	var options []SubscribeOption
	for _, po := range poptions {
		options = append(options, SubscribeOption{
			Service:     po.GetService(),
			MinDistance: po.GetMinDistance(),
			MaxDistance: po.GetMaxDistance(),
		})
	}
	return options
}

func rpcSubscribeOptionsToProto(options []SubscribeOption) []*protocol.SubscribeOption {
	var poptions []*protocol.SubscribeOption
	for _, o := range options {
		poptions = append(poptions, &protocol.SubscribeOption{
			Service:     proto.String(o.Service),
			MinDistance: proto.Uint32(o.MinDistance),
			MaxDistance: proto.Uint32(o.MaxDistance),
		})
	}
	return poptions
}

func rpcProtoToEndpoints(pendpoints []*protocol.Endpoint) []Endpoint {
	var endpoints []Endpoint
	for _, pe := range pendpoints {
		endpoints = append(endpoints, Endpoint{
			Network:  pe.GetNetwork(),
			Address:  pe.GetAddress(),
			TargetID: pe.GetTargetId(),
		})
	}
	return endpoints
}

func rpcEndpointsToProto(endpoints []Endpoint) []*protocol.Endpoint {
	var pendpoints []*protocol.Endpoint
	for _, e := range endpoints {
		pendpoint := &protocol.Endpoint{
			Network:  proto.String(e.Network),
			Address:  proto.String(e.Address),
			TargetId: proto.Int64(e.TargetID),
		}
		pendpoints = append(pendpoints, pendpoint)
	}
	return pendpoints
}

func rpcProtoToPublishSettings(settings *protocol.PublishSettings) PublishSettings {
	return PublishSettings{
		Priority:    settings.GetPriority(),
		Distance:    settings.GetDistance(),
		Concurrency: settings.GetConcurrency(),
		QueueSize:   settings.GetQueueSize(),
	}
}

func rpcPublishSettingsToProto(settings PublishSettings) *protocol.PublishSettings {
	return &protocol.PublishSettings{
		Priority:    proto.Uint32(settings.Priority),
		Distance:    proto.Uint32(settings.Distance),
		Concurrency: proto.Uint32(settings.Concurrency),
		QueueSize:   proto.Uint32(settings.QueueSize),
	}
}

func rpcProtoToRoutes(proutes []*protocol.Route) []Route {
	var routes []Route
	for _, proute := range proutes {
		routes = append(routes, Route{
			Options: rpcProtoToSubscribeOptions(proute.GetOptions()),
			Weight:  proute.GetWeight(),
		})
	}
	return routes
}

func rpcRoutesToProto(routes []Route) []*protocol.Route {
	var proutes []*protocol.Route
	for _, route := range routes {
		proute := &protocol.Route{
			Options: rpcSubscribeOptionsToProto(route.Options),
			Weight:  proto.Uint32(route.Weight),
		}
		proutes = append(proutes, proute)
	}
	return proutes
}

func rpcProtoToServiceChanges(pchanges []*protocol.ServiceChange) []ServiceChange {
	var changes []ServiceChange
	for _, pchange := range pchanges {
		changes = append(changes, ServiceChange{
			TargetID: pchange.GetTargetId(),
			Name:     pchange.GetName(),
			Settings: rpcProtoToPublishSettings(pchange.GetSettings()),
		})
	}
	return changes
}

func rpcServiceChangesToProto(changes []ServiceChange) []*protocol.ServiceChange {
	var pchanges []*protocol.ServiceChange
	for _, change := range changes {
		pchanges = append(pchanges, &protocol.ServiceChange{
			TargetId: proto.Int64(change.TargetID),
			Name:     proto.String(change.Name),
			Settings: rpcPublishSettingsToProto(change.Settings),
		})
	}
	return pchanges
}
