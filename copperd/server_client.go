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

	published   map[int64]string
	pubWatchers map[*serverServiceChangeStream]struct{}
}

var _ lowLevelServer = &serverClient{}

func newServerClient(s *server, conn net.Conn) *serverClient {
	c := &serverClient{
		owner:       s,
		published:   make(map[int64]string),
		pubWatchers: make(map[*serverServiceChangeStream]struct{}),
	}
	c.conn = copper.NewConn(conn, c, true)
	go c.serve()
	return c
}

func (c *serverClient) HandleStream(stream copper.Stream) {
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
}

func (c *serverClient) failWithErrorLocked(err error) {
	if c.failure == nil {
		c.failure = err
		c.conn.Close()
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

func (c *serverClient) subscribe(options ...SubscribeOption) (int64, error) {
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
	if oldName, ok := c.published[targetID]; ok {
		return fmt.Errorf("target %d is already published as %q", targetID, oldName)
	}
	err := c.owner.publishLocalLocked(name, c, targetID, settings)
	if err != nil {
		return fmt.Errorf("target %d cannot be published: %s", targetID, err)
	}
	c.published[targetID] = name
	return nil
}

func (c *serverClient) unpublish(targetID int64) error {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return c.failure
	}
	name, ok := c.published[targetID]
	if !ok {
		return fmt.Errorf("target %d is not published", targetID)
	}
	err := c.owner.unpublishLocalLocked(name, c, targetID)
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
