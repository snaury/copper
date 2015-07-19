package copper

import (
	"fmt"
	"net"
)

type serverClient struct {
	owner   *server
	conn    RawConn
	failure error

	allowChanges bool

	subscriptions map[int64]*serverSubscription

	published   map[int64]*localEndpoint
	pubWatchers map[*serverServiceChangesStream]struct{}
}

var _ lowLevelServer = &serverClient{}

func newServerClient(s *server, conn net.Conn, allowChanges bool) *serverClient {
	c := &serverClient{
		owner: s,

		allowChanges: allowChanges,

		subscriptions: make(map[int64]*serverSubscription),

		published:   make(map[int64]*localEndpoint),
		pubWatchers: make(map[*serverServiceChangesStream]struct{}),
	}
	c.conn = NewRawConn(conn, c, true)
	s.clientwg.Add(1)
	go c.serve()
	return c
}

func (c *serverClient) handleRequestWith(client Stream, endpoint endpointReference) error {
	status := endpoint.handleRequestLocked(func(remote Stream) handleRequestStatus {
		passthruBoth(client, remote)
		return handleRequestStatusDone
	})
	switch status {
	case handleRequestStatusDone:
		return nil
	case handleRequestStatusImpossible, handleRequestStatusNoRoute:
		// request failed or couldn't be routed
		return ENOROUTE
	case handleRequestStatusOverCapacity:
		// there is not enough capacity to handle the request
		return EOVERCAPACITY
	default:
		return fmt.Errorf("unhandled request status: %d", status)
	}
}

func (c *serverClient) ServeCopper(stream Stream) error {
	return rpcWrapServer(stream, c)
}

func (c *serverClient) closeWithErrorLocked(err error) {
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
	defer c.owner.clientwg.Done()
	<-c.conn.Done()
	err := c.conn.Err()
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	delete(c.owner.clients, c)
	c.closeWithErrorLocked(err)
}

func (c *serverClient) handleNewStream(targetID int64, stream Stream) error {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return c.failure
	}
	if sub := c.subscriptions[targetID]; sub != nil {
		// This is a subscription
		return c.handleRequestWith(stream, sub)
	}
	if pub := c.owner.pubByTarget[targetID]; pub != nil {
		// This is a direct connection
		return c.handleRequestWith(stream, pub)
	}
	return ENOTARGET
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
	return nil, EUNSUPPORTED
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
	if !c.allowChanges {
		return fmt.Errorf("publishing is not allowed")
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
	if !c.allowChanges {
		return fmt.Errorf("setting routes is not allowed")
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

func (c *serverClient) streamServices() (ServiceChangesStream, error) {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	cs := newServerServiceChangesStream(c.owner, c)
	return cs, nil
}
