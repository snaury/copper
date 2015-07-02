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

	published   map[int64]*localEndpoint
	pubWatchers map[*serverServiceChangeStream]struct{}
}

var _ lowLevelServer = &serverClient{}

func newServerClient(s *server, conn net.Conn) *serverClient {
	c := &serverClient{
		owner:       s,
		published:   make(map[int64]*localEndpoint),
		pubWatchers: make(map[*serverServiceChangeStream]struct{}),
	}
	c.conn = copper.NewConn(conn, c, true)
	go c.serve()
	return c
}

type possibleUpstream struct {
	owner    *server
	conn     copper.Conn
	targetID int64
	endpoint returnableEndpoint
}

func (upstream *possibleUpstream) returnUpstream() bool {
	upstream.owner.lock.Lock()
	defer upstream.owner.lock.Unlock()
	return upstream.endpoint.returnEndpointLocked()
}

func (c *serverClient) selectUpstream(targetID int64) (upstream possibleUpstream, err error) {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return possibleUpstream{}, c.failure
	}
	pub := c.owner.pubByTarget[targetID]
	if pub != nil {
		// This is a direct connection
		if len(pub.localEndpoints) == 0 {
			// But there are no endpoints currently published
			return possibleUpstream{}, copper.ENOROUTE
		}
		endpoint, err := pub.selectLocalEndpoint()
		if err != nil {
			return possibleUpstream{}, err
		}
		return possibleUpstream{
			owner:    c.owner,
			conn:     endpoint.key.client.conn,
			targetID: endpoint.key.targetID,
			endpoint: endpoint,
		}, nil
	}
	// TODO: support for subscription targets
	return possibleUpstream{}, copper.ENOTARGET
}

func (c *serverClient) HandleStream(stream copper.Stream) {
	if stream.TargetID() == 0 {
		// This is our rpc control target
		err := rpcWrapServer(stream, c)
		if err != nil {
			_, ok := err.(copper.Error)
			if !ok {
				err = rpcError{
					error: err,
					code:  copper.EINTERNAL,
				}
			}
			stream.CloseWithError(err)
		}
		return
	}
	// Need to find an upstream and forward the data
	upstream, err := c.selectUpstream(stream.TargetID())
	if err != nil {
		stream.CloseWithError(err)
		return
	}
	defer upstream.returnUpstream()

	remote, err := upstream.conn.Open(upstream.targetID)
	if err != nil {
		stream.CloseWithError(err)
		return
	}
	passthruBoth(stream, remote)
}

func (c *serverClient) failWithErrorLocked(err error) {
	if c.failure == nil {
		c.failure = err
		c.conn.Close()
		for targetID, endpoint := range c.published {
			delete(c.published, targetID)
			endpoint.unregisterLocked()
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
	c.failure = err
}

func (c *serverClient) subscribe(settings SubscribeSettings) (int64, error) {
	return 0, ErrUnsupported
}

func (c *serverClient) getEndpoints(targetID int64) ([]Endpoint, error) {
	return nil, ErrUnsupported
}

func (c *serverClient) streamEndpoints(targetID int64) (EndpointChangesStream, error) {
	return nil, ErrUnsupported
}

func (c *serverClient) unsubscribe(targetID int64) error {
	return ErrUnsupported
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
	key := localEndpointKey{
		client:   c,
		targetID: targetID,
	}
	endpoint, err := c.owner.publishLocalLocked(name, key, settings)
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
	err := endpoint.unregisterLocked()
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
	if len(routes) > 0 {
		c.owner.routes[name] = routes
	} else {
		delete(c.owner.routes, name)
	}
	return nil
}

func (c *serverClient) listRoutes() ([]string, error) {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	names := make([]string, 0, len(c.owner.routes))
	for name := range c.owner.routes {
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
	return c.owner.routes[name], nil
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
