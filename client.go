package copper

import (
	"net"
)

type clientConn struct {
	rpcClient
	hmap *HandlerMap
}

var _ Client = &clientConn{}

func newClient(conn net.Conn) *clientConn {
	c := &clientConn{
		hmap: NewHandlerMap(nil),
	}
	c.rpcClient.RawConn = NewRawConn(conn, c, false)
	return c
}

// NewClient wraps an existing connection and returns a copper client
func NewClient(conn net.Conn) Client {
	return newClient(conn)
}

type clientSubscription struct {
	owner    *clientConn
	targetID int64
}

func (sub *clientSubscription) Endpoints() ([]Endpoint, error) {
	return sub.owner.getEndpoints(sub.targetID)
}

func (sub *clientSubscription) EndpointChanges() (EndpointChangesStream, error) {
	return sub.owner.streamEndpoints(sub.targetID)
}

func (sub *clientSubscription) Open() (Stream, error) {
	return sub.owner.openNewStream(sub.targetID)
}

func (sub *clientSubscription) Stop() error {
	return sub.owner.unsubscribe(sub.targetID)
}

func (c *clientConn) Subscribe(settings SubscribeSettings) (Subscription, error) {
	targetID, err := c.subscribe(settings)
	if err != nil {
		return nil, err
	}
	return &clientSubscription{
		owner:    c,
		targetID: targetID,
	}, nil
}

type clientPublication struct {
	owner    *clientConn
	targetID int64
}

func (pub *clientPublication) Stop() error {
	err := pub.owner.unpublish(pub.targetID)
	pub.owner.hmap.Remove(pub.targetID)
	return err
}

func (c *clientConn) Publish(name string, settings PublishSettings, handler Handler) (Publication, error) {
	targetID := c.hmap.Add(handler)
	err := c.publish(targetID, name, settings)
	if err != nil {
		c.hmap.Remove(targetID)
		return nil, err
	}
	return &clientPublication{
		owner:    c,
		targetID: targetID,
	}, nil
}

func (c *clientConn) SetRoute(name string, routes ...Route) error {
	return c.setRoute(name, routes...)
}

func (c *clientConn) ListRoutes() ([]string, error) {
	return c.listRoutes()
}

func (c *clientConn) LookupRoute(name string) ([]Route, error) {
	return c.lookupRoute(name)
}

func (c *clientConn) ServiceChanges() (ServiceChangesStream, error) {
	return c.streamServices()
}

func (c *clientConn) ServeCopper(stream Stream) error {
	return rpcWrapClient(stream, c.hmap)
}
