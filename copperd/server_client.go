package copperd

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

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

type possibleUpstream struct {
	conn     copper.Conn
	targetID int64
}

func (c *serverClient) selectUpstreams(targetID int64) (upstreams []possibleUpstream, err error) {
	c.owner.lock.Lock()
	defer c.owner.lock.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	pub := c.owner.pubByTarget[targetID]
	if pub != nil {
		// This is a direct connection
		for client, endpoints := range pub.localEndpoints {
			for clientTargetID, settings := range endpoints {
				if settings.Concurrency > 0 {
					upstreams = append(upstreams, possibleUpstream{
						conn:     client.conn,
						targetID: clientTargetID,
					})
				}
			}
		}
		return
	}
	// Might be a subscription
	return nil, copper.ENOTARGET
}

func passthru(dst, src copper.Stream) {
	var writeclosed uint32
	go func() {
		err := dst.WaitWriteClosed()
		atomic.AddUint32(&writeclosed, 1)
		if err == copper.ESTREAMCLOSED {
			src.CloseRead()
		} else {
			src.CloseReadError(err)
		}
	}()
	for {
		buf, err := src.Peek()
		if atomic.LoadUint32(&writeclosed) > 0 {
			// Don't react to CloseRead in the above goroutine
			return
		}
		if len(buf) > 0 {
			n, werr := dst.Write(buf)
			if n > 0 {
				src.Discard(n)
			}
			if werr != nil {
				if werr == copper.ESTREAMCLOSED {
					src.CloseRead()
				} else {
					src.CloseReadError(werr)
				}
				return
			}
		}
		if err != nil {
			if err == io.EOF {
				dst.CloseWrite()
			} else {
				dst.CloseWithError(err)
			}
			return
		}
	}
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
	upstreams, err := c.selectUpstreams(stream.TargetID())
	if err != nil {
		stream.CloseWithError(err)
		return
	}
	for _, upstream := range upstreams {
		dst, err := upstream.conn.Open(upstream.targetID)
		if err != nil {
			continue
		}
		defer dst.Close()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			passthru(dst, stream)
		}()
		go func() {
			defer wg.Done()
			passthru(stream, dst)
		}()
		wg.Wait()
		return
	}
	stream.CloseWithError(copper.ENOROUTE)
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
