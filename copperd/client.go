package copperd

import (
	"net"

	"github.com/snaury/copper"
)

type client struct {
	rpcClient
	hmap *copper.StreamHandlerMap
}

var _ Client = &client{}

func newClient(conn net.Conn) *client {
	hmap := copper.NewStreamHandlerMap(nil)
	return &client{
		rpcClient: rpcClient{
			Conn:     copper.NewConn(conn, hmap, false),
			targetID: 0,
		},
		hmap: hmap,
	}
}

func dialClient(network, address string) (*client, error) {
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	return newClient(conn), nil
}

type clientSubscription struct {
	owner    *client
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

func (c *client) Subscribe(options ...SubscribeOption) (Subscription, error) {
	targetID, err := c.subscribe(options...)
	if err != nil {
		return nil, err
	}
	return &clientSubscription{
		owner:    c,
		targetID: targetID,
	}, nil
}

type clientPublication struct {
	owner    *client
	targetID int64
}

func (pub *clientPublication) Stop() error {
	err := pub.owner.unpublish(pub.targetID)
	pub.owner.hmap.Remove(pub.targetID)
	return err
}

func (c *client) Publish(name string, settings PublishSettings, handler copper.StreamHandler) (Publication, error) {
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

func (c *client) SetRoute(name string, routes ...Route) error {
	return c.setRoute(name, routes...)
}

func (c *client) LookupRoute(name string) ([]Route, error) {
	return c.lookupRoute(name)
}

func (c *client) ServiceChanges() (ServiceChangeStream, error) {
	return c.streamServices()
}
