package copper

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/snaury/copper/protocol"
)

func rpcWrapClient(stream Stream, hmap *HandlerMap) error {
	rtype, err := rpcReadRequestType(stream)
	if err != nil {
		return EINVALID
	}
	switch rtype {
	case protocol.RequestType_NewStream:
		var buf [8]byte
		_, err = stream.Read(buf[0:8])
		if err != nil {
			return EINVALID
		}
		targetID := int64(binary.BigEndian.Uint64(buf[0:8]))
		handler := hmap.Find(targetID)
		if handler == nil {
			return ENOTARGET
		}
		stream.Acknowledge()
		return handler.ServeCopper(stream)
	}
	return copperError{
		error: fmt.Errorf("unsupported request type %d", rtype),
		code:  EUNSUPPORTED,
	}
}

type rpcClient struct {
	RawConn
}

var _ lowLevelClient = &rpcClient{}

func (c *rpcClient) openNewStream(targetID int64) (Stream, error) {
	return rpcNewStream(c.RawConn, targetID)
}

func (c *rpcClient) subscribe(settings SubscribeSettings) (int64, error) {
	var response protocol.SubscribeResponse
	err := rpcSimpleRequest(
		c.RawConn,
		protocol.RequestType_Subscribe,
		&protocol.SubscribeRequest{
			Options:       rpcSubscribeOptionsToProto(settings.Options),
			DisableRoutes: settings.DisableRoutes,
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
		c.RawConn,
		protocol.RequestType_GetEndpoints,
		&protocol.GetEndpointsRequest{
			TargetId: targetID,
		},
		&response,
	)
	if err != nil {
		return nil, err
	}
	return rpcProtoToEndpoints(response.GetEndpoints()), nil
}

type rpcEndpointChangesStream struct {
	stream  Stream
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
		c.RawConn,
		protocol.RequestType_StreamEndpoints,
		&protocol.StreamEndpointsRequest{
			TargetId: targetID,
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
		c.RawConn,
		protocol.RequestType_Unsubscribe,
		&protocol.UnsubscribeRequest{
			TargetId: targetID,
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
		c.RawConn,
		protocol.RequestType_Publish,
		&protocol.PublishRequest{
			TargetId: targetID,
			Name:     name,
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
		c.RawConn,
		protocol.RequestType_Unpublish,
		&protocol.UnpublishRequest{
			TargetId: targetID,
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
		c.RawConn,
		protocol.RequestType_SetRoute,
		&protocol.SetRouteRequest{
			Name:   name,
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
		c.RawConn,
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
		c.RawConn,
		protocol.RequestType_LookupRoute,
		&protocol.LookupRouteRequest{
			Name: name,
		},
		&response,
	)
	if err != nil {
		return nil, err
	}
	return rpcProtoToRoutes(response.GetRoutes()), nil
}

type rpcServiceChangesStream struct {
	stream  Stream
	results chan ServiceChanges
	err     error
}

func (s *rpcServiceChangesStream) readloop() {
	defer s.stream.Close()
	defer close(s.results)
	for {
		var response protocol.StreamServicesResponse
		err := rpcReadMessage(s.stream, &response)
		if err != nil {
			s.err = err
			break
		}
		s.results <- ServiceChanges{
			Removed: response.GetRemoved(),
			Changed: rpcProtoToServiceChanges(response.GetChanged()),
		}
	}
}

func (s *rpcServiceChangesStream) Read() (ServiceChanges, error) {
	if result, ok := <-s.results; ok {
		return result, nil
	}
	return ServiceChanges{}, s.err
}

func (s *rpcServiceChangesStream) Stop() error {
	return s.stream.Close()
}

func (c *rpcClient) streamServices() (ServiceChangesStream, error) {
	stream, err := rpcStreamingRequest(
		c.RawConn,
		protocol.RequestType_StreamServices,
		&protocol.StreamServicesRequest{},
	)
	if err != nil {
		return nil, err
	}
	changes := &rpcServiceChangesStream{
		stream:  stream,
		results: make(chan ServiceChanges, 16),
		err:     io.EOF,
	}
	go changes.readloop()
	return changes, nil
}
