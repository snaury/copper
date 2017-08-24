package copper

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/golang/protobuf/proto"
	"github.com/snaury/copper/protocol"
)

func rpcWrapServer(stream Stream, server lowLevelServer) error {
	rtype, readerr := rpcReadRequestType(stream)
	if readerr != nil {
		return EINVALID
	}
	switch rtype {
	case protocol.RequestType_NewStream:
		var buf [8]byte
		_, err := stream.Read(buf[0:8])
		if err != nil {
			return EINVALID
		}
		targetID := binary.BigEndian.Uint64(buf[0:8])
		return server.handleNewStream(int64(targetID), stream)
	case protocol.RequestType_Subscribe:
		var request protocol.SubscribeRequest
		err := rpcReadMessage(stream, &request)
		if err != nil {
			return EINVALID
		}
		targetID, err := server.subscribe(SubscribeSettings{
			Options:       rpcProtoToSubscribeOptions(request.GetOptions()),
			DisableRoutes: request.GetDisableRoutes(),
		})
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.SubscribeResponse{
			TargetId: proto.Int64(targetID),
		})
	case protocol.RequestType_GetEndpoints:
		var request protocol.GetEndpointsRequest
		err := rpcReadMessage(stream, &request)
		if err != nil {
			return EINVALID
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
		err := rpcReadMessage(stream, &request)
		if err != nil {
			return EINVALID
		}
		changes, err := server.streamEndpoints(request.GetTargetId())
		if err != nil {
			return err
		}
		defer changes.Stop()
		go func() {
			// this goroutine detects when write side is closed, which closes
			// the changes stream and unblocks a read below
			defer changes.Stop()
			<-stream.WriteClosed()
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
		err := rpcReadMessage(stream, &request)
		if err != nil {
			return EINVALID
		}
		err = server.unsubscribe(request.GetTargetId())
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.UnsubscribeResponse{})
	case protocol.RequestType_Publish:
		var request protocol.PublishRequest
		err := rpcReadMessage(stream, &request)
		if err != nil {
			return EINVALID
		}
		err = server.publish(
			request.GetTargetId(),
			request.GetName(),
			rpcProtoToPublishSettings(request.GetSettings()),
		)
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.PublishResponse{})
	case protocol.RequestType_Unpublish:
		var request protocol.UnpublishRequest
		err := rpcReadMessage(stream, &request)
		if err != nil {
			return EINVALID
		}
		err = server.unpublish(request.GetTargetId())
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.UnpublishResponse{})
	case protocol.RequestType_SetRoute:
		var request protocol.SetRouteRequest
		err := rpcReadMessage(stream, &request)
		if err != nil {
			return EINVALID
		}
		err = server.setRoute(request.GetName(), rpcProtoToRoutes(request.GetRoutes())...)
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.SetRouteResponse{})
	case protocol.RequestType_ListRoutes:
		var request protocol.ListRoutesRequest
		err := rpcReadMessage(stream, &request)
		if err != nil {
			return EINVALID
		}
		names, err := server.listRoutes()
		if err != nil {
			return err
		}
		return rpcWriteMessage(stream, &protocol.ListRoutesResponse{
			Names: names,
		})
	case protocol.RequestType_LookupRoute:
		var request protocol.LookupRouteRequest
		err := rpcReadMessage(stream, &request)
		if err != nil {
			return EINVALID
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
		err := rpcReadMessage(stream, &request)
		if err != nil {
			return EINVALID
		}
		changes, err := server.streamServices()
		if err != nil {
			return err
		}
		defer changes.Stop()
		go func() {
			// this goroutine detects when read side is closed, which closes
			// the changes stream and unblocks a read below
			defer changes.Stop()
			<-stream.WriteClosed()
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
				Removed: result.Removed,
				Changed: rpcServiceChangesToProto(result.Changed),
			})
			if err != nil {
				return err
			}
		}
	}
	return copperError{
		error: fmt.Errorf("unsupported request type %d", rtype),
		code:  EUNSUPPORTED,
	}
}
