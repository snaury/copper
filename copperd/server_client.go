package copperd

import (
	"fmt"
	"net"

	"github.com/snaury/copper"
)

type serverClient struct {
	owner   *server
	conn    copper.Conn
	failure error

	subscriptions map[int64]*serverSubscription

	published   map[int64]*localEndpoint
	pubWatchers map[*serverServiceChangeStream]struct{}
}

var _ lowLevelServer = &serverClient{}

func newServerClient(s *server, conn net.Conn) *serverClient {
	c := &serverClient{
		owner: s,

		subscriptions: make(map[int64]*serverSubscription),

		published:   make(map[int64]*localEndpoint),
		pubWatchers: make(map[*serverServiceChangeStream]struct{}),
	}
	c.conn = copper.NewConn(conn, c, true)
	go c.serve()
	return c
}

func (c *serverClient) handleControl(client copper.Stream) error {
	err := rpcWrapServer(client, c)
	if err != nil {
		_, ok := err.(copper.Error)
		if !ok {
			err = rpcError{
				error: err,
				code:  copper.EINTERNAL,
			}
		}
	}
	return err
}

func (c *serverClient) handleRequest(client copper.Stream) error {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return c.failure
	}
	if sub := c.subscriptions[client.TargetID()]; sub != nil {
		// This is a subscription
		if sub.handleRequestLocked(client) {
			return nil
		}
		return copper.ENOROUTE
	}
	if pub := c.owner.pubByTarget[client.TargetID()]; pub != nil {
		// THis is a direct connection
		if pub.handleRequestLocked(client) {
			return nil
		}
		return copper.ENOROUTE
	}
	return copper.ENOTARGET
}

func (c *serverClient) HandleStream(stream copper.Stream) {
	var err error
	if stream.TargetID() == 0 {
		err = c.handleControl(stream)
	} else {
		err = c.handleRequest(stream)
	}
	if err != nil {
		stream.CloseWithError(err)
	}
}

func (c *serverClient) failWithErrorLocked(err error) {
	if c.failure == nil {
		c.failure = err
		c.conn.Close()
		for targetID, sub := range c.subscriptions {
			delete(c.subscriptions, targetID)
			sub.unsubscribeLocked()
		}
		for targetID, endpoint := range c.published {
			delete(c.published, targetID)
			endpoint.unpublishLocked()
		}
		for cs := range c.pubWatchers {
			cs.stopLocked()
		}
	}
}

func (c *serverClient) serve() {
	err := c.conn.Wait()
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	delete(c.owner.clients, c)
	c.failWithErrorLocked(err)
}

func (c *serverClient) subscribe(settings SubscribeSettings) (int64, error) {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return 0, c.failure
	}
	sub, err := c.owner.subscribeLocked(settings)
	if err != nil {
		return 0, err
	}
	c.subscriptions[sub.targetID] = sub
	return sub.targetID, nil
}

func (c *serverClient) getEndpoints(targetID int64) ([]Endpoint, error) {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	sub := c.subscriptions[targetID]
	if sub == nil {
		return nil, fmt.Errorf("target %d is not subscribed", targetID)
	}
	return sub.getEndpointsLocked(), nil
}

func (c *serverClient) streamEndpoints(targetID int64) (EndpointChangesStream, error) {
	return nil, ErrUnsupported
}

func (c *serverClient) unsubscribe(targetID int64) error {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return c.failure
	}
	sub := c.subscriptions[targetID]
	if sub == nil {
		return fmt.Errorf("target %d is not subscribed", targetID)
	}
	delete(c.subscriptions, targetID)
	sub.unsubscribeLocked()
	return nil
}

func (c *serverClient) publish(targetID int64, name string, settings PublishSettings) error {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return c.failure
	}
	if old := c.published[targetID]; old != nil && old.pub != nil {
		return fmt.Errorf("target %d is already published as %q", targetID, old.pub.name)
	}
	if settings.Concurrency == 0 {
		return fmt.Errorf("publishing with concurrency=0 is not alloswed")
	}
	key := localEndpointKey{
		client:   c,
		targetID: targetID,
	}
	endpoint, err := c.owner.publishLocked(name, key, settings)
	if err != nil {
		return fmt.Errorf("target %d cannot be published: %s", targetID, err)
	}
	c.published[targetID] = endpoint
	return nil
}

func (c *serverClient) unpublish(targetID int64) error {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return c.failure
	}
	endpoint := c.published[targetID]
	if endpoint == nil {
		return fmt.Errorf("target %d is not published", targetID)
	}
	delete(c.published, targetID)
	err := endpoint.unpublishLocked()
	if err != nil {
		return fmt.Errorf("target %d cannot be unpublished: %s", targetID, err)
	}
	delete(c.published, targetID)
	return nil
}

func (c *serverClient) setRoute(name string, routes ...Route) error {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return c.failure
	}
	return c.owner.setRouteLocked(name, routes)
}

func (c *serverClient) listRoutes() ([]string, error) {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	names := make([]string, 0, len(c.owner.routeByName))
	for name := range c.owner.routeByName {
		names = append(names, name)
	}
	return names, nil
}

func (c *serverClient) lookupRoute(name string) ([]Route, error) {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	if r := c.owner.routeByName[name]; r != nil {
		return r.routes, nil
	}
	return nil, nil
}

func (c *serverClient) streamServices() (ServiceChangeStream, error) {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	cs := newServerServiceChangeStream(c.owner, c)
	return cs, nil
}
