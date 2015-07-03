package copperd

import (
	"net"

	"github.com/snaury/copper"
)

type clientConn struct {
	rpcClient
	hmap *copper.StreamHandlerMap
}

var _ Client = &clientConn{}

func newClient(conn net.Conn) *clientConn {
	hmap := copper.NewStreamHandlerMap(nil)
	return &clientConn{
		rpcClient: rpcClient{
			Conn:     copper.NewConn(conn, hmap, false),
			targetID: 0,
		},
		hmap: hmap,
	}
}

// NewClient wraps an existing connection and returns a copperd client
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

func (sub *clientSubscription) Open() (copper.Stream, error) {
	return sub.owner.Open(sub.targetID)
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

func (c *clientConn) Publish(name string, settings PublishSettings, handler copper.StreamHandler) (Publication, error) {
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

func (c *clientConn) LookupRoute(name string) ([]Route, error) {
	return c.lookupRoute(name)
}

func (c *clientConn) ServiceChanges() (ServiceChangesStream, error) {
	return c.streamServices()
}

func (c *clientConn) Serve() error {
	return c.Conn.Wait()
}
