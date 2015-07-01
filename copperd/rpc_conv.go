package copperd

import (
	"github.com/golang/protobuf/proto"
	"github.com/snaury/copper/copperd/protocol"
)

func rpcProtoToSubscribeOptions(poptions []*protocol.SubscribeOption) []SubscribeOption {
	var options []SubscribeOption
	for _, po := range poptions {
		options = append(options, SubscribeOption{
			Service:    po.GetService(),
			Distance:   po.GetDistance(),
			MaxRetries: po.GetMaxRetries(),
		})
	}
	return options
}

func rpcSubscribeOptionsToProto(options []SubscribeOption) []*protocol.SubscribeOption {
	var poptions []*protocol.SubscribeOption
	for _, o := range options {
		poptions = append(poptions, &protocol.SubscribeOption{
			Service:    proto.String(o.Service),
			Distance:   proto.Uint32(o.Distance),
			MaxRetries: proto.Uint32(o.MaxRetries),
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

func rpcProtoToRoutes(proutes []*protocol.Route) []Route {
	var routes []Route
	for _, proute := range proutes {
		routes = append(routes, Route{
			Service:  proute.GetService(),
			Weight:   proute.GetWeight(),
			Distance: proute.GetDistance(),
		})
	}
	return routes
}

func rpcRoutesToProto(routes []Route) []*protocol.Route {
	var proutes []*protocol.Route
	for _, route := range routes {
		proute := &protocol.Route{
			Service:  proto.String(route.Service),
			Weight:   proto.Uint32(route.Weight),
			Distance: proto.Uint32(route.Distance),
		}
		proutes = append(proutes, proute)
	}
	return proutes
}