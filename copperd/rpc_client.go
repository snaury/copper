package copperd

import (
	"io"

	"github.com/golang/protobuf/proto"
	"github.com/snaury/copper"
	"github.com/snaury/copper/copperd/protocol"
)

type rpcClient struct {
	copper.Conn
	targetID int64
}

var _ lowLevelServer = &rpcClient{}

func (c *rpcClient) subscribe(settings SubscribeSettings) (int64, error) {
	var response protocol.SubscribeResponse
	err := rpcSimpleRequest(
		c.Conn,
		c.targetID,
		protocol.RequestType_Subscribe,
		&protocol.SubscribeRequest{
			Options:       rpcSubscribeOptionsToProto(settings.Options),
			MaxRetries:    proto.Uint32(settings.MaxRetries),
			DisableRoutes: proto.Bool(settings.DisableRoutes),
		},
		&response,
	)
	if err != nil {
		return 0, err
	}
	return response.GetTargetId(), nil
}

func (c *rpcClient) getEndpoints(targetID int64) ([]Endpoint, error) {
	var response protocol.GetEndpointsResponse
	err := rpcSimpleRequest(
		c.Conn,
		c.targetID,
		protocol.RequestType_GetEndpoints,
		&protocol.GetEndpointsRequest{
			TargetId: proto.Int64(targetID),
		},
		&response,
	)
	if err != nil {
		return nil, err
	}
	return rpcProtoToEndpoints(response.GetEndpoints()), nil
}

type rpcEndpointChangesStream struct {
	stream  copper.Stream
	results chan EndpointChanges
	err     error
}

func (s *rpcEndpointChangesStream) readloop() {
	defer s.stream.Close()
	defer close(s.results)
	for {
		var response protocol.StreamEndpointsResponse
		err := rpcReadMessage(s.stream, &response)
		if err != nil {
			s.err = err
			break
		}
		s.results <- EndpointChanges{
			Added:   rpcProtoToEndpoints(response.GetAdded()),
			Removed: rpcProtoToEndpoints(response.GetRemoved()),
		}
	}
}

func (s *rpcEndpointChangesStream) Read() (EndpointChanges, error) {
	if result, ok := <-s.results; ok {
		return result, nil
	}
	return EndpointChanges{}, s.err
}

func (s *rpcEndpointChangesStream) Stop() error {
	return s.stream.Close()
}

func (c *rpcClient) streamEndpoints(targetID int64) (EndpointChangesStream, error) {
	stream, err := rpcStreamingRequest(
		c.Conn,
		c.targetID,
		protocol.RequestType_StreamEndpoints,
		&protocol.StreamEndpointsRequest{
			TargetId: proto.Int64(targetID),
		},
	)
	if err != nil {
		return nil, err
	}
	changes := &rpcEndpointChangesStream{
		stream:  stream,
		results: make(chan EndpointChanges, 16),
		err:     io.EOF,
	}
	go changes.readloop()
	return changes, nil
}

func (c *rpcClient) unsubscribe(targetID int64) error {
	var response protocol.UnsubscribeResponse
	err := rpcSimpleRequest(
		c.Conn,
		c.targetID,
		protocol.RequestType_Unsubscribe,
		&protocol.UnsubscribeRequest{
			TargetId: proto.Int64(targetID),
		},
		&response,
	)
	if err != nil {
		return err
	}
	return nil
}

func (c *rpcClient) publish(targetID int64, name string, settings PublishSettings) error {
	var response protocol.PublishResponse
	err := rpcSimpleRequest(
		c.Conn,
		c.targetID,
		protocol.RequestType_Publish,
		&protocol.PublishRequest{
			TargetId: proto.Int64(targetID),
			Name:     proto.String(name),
			Settings: rpcPublishSettingsToProto(settings),
		},
		&response,
	)
	if err != nil {
		return err
	}
	return nil
}

func (c *rpcClient) unpublish(targetID int64) error {
	var response protocol.UnpublishResponse
	err := rpcSimpleRequest(
		c.Conn,
		c.targetID,
		protocol.RequestType_Unpublish,
		&protocol.UnpublishRequest{
			TargetId: proto.Int64(targetID),
		},
		&response,
	)
	if err != nil {
		return err
	}
	return nil
}

func (c *rpcClient) setRoute(name string, routes ...Route) error {
	var response protocol.SetRouteResponse
	err := rpcSimpleRequest(
		c.Conn,
		c.targetID,
		protocol.RequestType_SetRoute,
		&protocol.SetRouteRequest{
			Name:   proto.String(name),
			Routes: rpcRoutesToProto(routes),
		},
		&response,
	)
	if err != nil {
		return err
	}
	return nil
}

func (c *rpcClient) listRoutes() ([]string, error) {
	var response protocol.ListRoutesResponse
	err := rpcSimpleRequest(
		c.Conn,
		c.targetID,
		protocol.RequestType_ListRoutes,
		&protocol.ListRoutesRequest{},
		&response,
	)
	if err != nil {
		return nil, err
	}
	return response.GetNames(), nil
}

func (c *rpcClient) lookupRoute(name string) ([]Route, error) {
	var response protocol.LookupRouteResponse
	err := rpcSimpleRequest(
		c.Conn,
		c.targetID,
		protocol.RequestType_LookupRoute,
		&protocol.LookupRouteRequest{
			Name: proto.String(name),
		},
		&response,
	)
	if err != nil {
		return nil, err
	}
	return rpcProtoToRoutes(response.GetRoutes()), nil
}

type rpcServiceChangeStream struct {
	stream  copper.Stream
	results chan ServiceChange
	err     error
}

func (s *rpcServiceChangeStream) readloop() {
	defer s.stream.Close()
	defer close(s.results)
	for {
		var response protocol.StreamServicesResponse
		err := rpcReadMessage(s.stream, &response)
		if err != nil {
			s.err = err
			break
		}
		s.results <- ServiceChange{
			TargetID: response.GetTargetId(),
			Name:     response.GetName(),
			Settings: rpcProtoToPublishSettings(response.GetSettings()),
			Valid:    response.GetValid(),
		}
	}
}

func (s *rpcServiceChangeStream) Read() (ServiceChange, error) {
	if result, ok := <-s.results; ok {
		return result, nil
	}
	return ServiceChange{}, s.err
}

func (s *rpcServiceChangeStream) Stop() error {
	return s.stream.Close()
}

func (c *rpcClient) streamServices() (ServiceChangeStream, error) {
	stream, err := rpcStreamingRequest(
		c.Conn,
		c.targetID,
		protocol.RequestType_StreamServices,
		&protocol.StreamServicesRequest{},
	)
	if err != nil {
		return nil, err
	}
	changes := &rpcServiceChangeStream{
		stream:  stream,
		results: make(chan ServiceChange, 16),
		err:     io.EOF,
	}
	go changes.readloop()
	return changes, nil
}
