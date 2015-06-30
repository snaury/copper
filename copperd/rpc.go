package copperd

import (
	"encoding/binary"
	"fmt"
	"github.com/golang/protobuf/proto"
	"github.com/snaury/copper"
	"github.com/snaury/copper/copperd/protocol"
	"io"
)

type rpcError struct {
	error
	code copper.ErrorCode
}

var _ copper.Error = rpcError{}

func (e rpcError) ErrorCode() copper.ErrorCode {
	return e.code
}

func rpcReadRequestType(r io.Reader) (rtype protocol.RequestType, err error) {
	var buf [1]byte
	_, err = io.ReadFull(r, buf[0:1])
	if err != nil {
		return
	}
	return protocol.RequestType(buf[0]), nil
}

func rpcReadMessage(r io.Reader, pb proto.Message) error {
	var buf [4]byte
	_, err := io.ReadFull(r, buf[0:4])
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	size := int(binary.LittleEndian.Uint32(buf[0:4]))
	var data []byte
	if size > 0 {
		data = make([]byte, size)
		_, err = io.ReadFull(r, data)
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
	} else if size < 0 {
		return copper.EINVALIDDATA
	}
	return proto.Unmarshal(data, pb)
}

func rpcWriteMessage(w io.Writer, pb proto.Message) error {
	data, err := proto.Marshal(pb)
	if err != nil {
		return err
	}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(data)))
	_, err = w.Write(buf[0:4])
	if err != nil {
		return err
	}
	if len(data) > 0 {
		_, err = w.Write(data)
		if err != nil {
			return err
		}
	}
	return nil
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

func rpcWrapServer(stream copper.Stream, server lowLevelServer) error {
	rtype, err := rpcReadRequestType(stream)
	if err != nil {
		return copper.EINVALIDDATA
	}
	switch rtype {
	case protocol.RequestType_Subscribe:
		var request protocol.SubscribeRequest
		err = rpcReadMessage(stream, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		var options []SubscribeOption
		for _, poption := range request.GetOptions() {
			options = append(options, SubscribeOption{
				Service:    poption.GetService(),
				Distance:   poption.GetDistance(),
				MaxRetries: poption.GetMaxRetries(),
			})
		}
		targetID, err := server.subscribe(options...)
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.SubscribeResponse{
			TargetId: proto.Int64(targetID),
		})
	case protocol.RequestType_GetEndpoints:
		var request protocol.GetEndpointsRequest
		err = rpcReadMessage(stream, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		endpoints, err := server.getEndpoints(request.GetTargetId())
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.GetEndpointsResponse{
			Endpoints: rpcEndpointsToProto(endpoints),
		})
	case protocol.RequestType_StreamEndpoints:
		var request protocol.StreamEndpointsRequest
		err = rpcReadMessage(stream, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		changes, err := server.streamEndpoints(request.GetTargetId())
		if err != nil {
			return err
		}
		defer changes.Stop()
		go func() {
			// this goroutine detects when read side is closed, which closes
			// the changes stream and unblocks a read below
			defer changes.Stop()
			stream.Peek()
		}()
		for {
			result, err := changes.Read()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			err = rpcWriteMessage(stream, &protocol.StreamEndpointsResponse{
				Added:   rpcEndpointsToProto(result.Added),
				Removed: rpcEndpointsToProto(result.Removed),
			})
			if err != nil {
				return err
			}
		}
	case protocol.RequestType_Unsubscribe:
		var request protocol.UnsubscribeRequest
		err = rpcReadMessage(stream, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		err = server.unsubscribe(request.GetTargetId())
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.UnsubscribeResponse{})
	case protocol.RequestType_Publish:
		var request protocol.PublishRequest
		err = rpcReadMessage(stream, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		err = server.publish(request.GetTargetId(), PublishSettings{
			Name:        request.GetName(),
			Distance:    request.GetDistance(),
			Concurrency: request.GetConcurrency(),
		})
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.PublishResponse{})
	case protocol.RequestType_Unpublish:
		var request protocol.UnpublishRequest
		err = rpcReadMessage(stream, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		err = server.unpublish(request.GetTargetId())
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.UnpublishResponse{})
	case protocol.RequestType_SetRoute:
		var request protocol.SetRouteRequest
		err = rpcReadMessage(stream, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		err = server.setRoute(request.GetName(), rpcProtoToRoutes(request.GetRoutes())...)
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.SetRouteResponse{})
	case protocol.RequestType_LookupRoute:
		var request protocol.LookupRouteRequest
		err = rpcReadMessage(stream, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		routes, err := server.lookupRoute(request.GetName())
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.LookupRouteResponse{
			Routes: rpcRoutesToProto(routes),
		})
	case protocol.RequestType_StreamServices:
		var request protocol.StreamServicesRequest
		err = rpcReadMessage(stream, &request)
		if err != nil {
			return copper.EINVALIDDATA
		}
		changes, err := server.streamServiceChanges()
		if err != nil {
			return err
		}
		defer changes.Stop()
		go func() {
			// this goroutine detects when read side is closed, which closes
			// the changes stream and unblocks a read below
			defer changes.Stop()
			stream.Peek()
		}()
		for {
			result, err := changes.Read()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			err = rpcWriteMessage(stream, &protocol.StreamServicesResponse{
				TargetId:    proto.Int64(result.TargetID),
				Name:        proto.String(result.Name),
				Distance:    proto.Uint32(result.Distance),
				Concurrency: proto.Uint32(result.Concurrency),
			})
			if err != nil {
				return err
			}
		}
	}
	return rpcError{
		error: fmt.Errorf("unsupported request type %d", rtype),
		code:  copper.EUNSUPPORTED,
	}
}
