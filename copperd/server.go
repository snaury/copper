package copperd

import (
	"fmt"
	"net"
	"sync"
)

type lowLevelServer interface {
	subscribe(settings SubscribeSettings) (int64, error)
	getEndpoints(targetID int64) ([]Endpoint, error)
	streamEndpoints(targetID int64) (EndpointChangesStream, error)
	unsubscribe(targetID int64) error
	publish(targetID int64, name string, settings PublishSettings) error
	unpublish(targetID int64) error
	setRoute(name string, routes ...Route) error
	listRoutes() ([]string, error)
	lookupRoute(name string) ([]Route, error)
	streamServices() (ServiceChangeStream, error)
}

type server struct {
	lock sync.Mutex

	nextTargetID int64

	pubByName   map[string]*serverPublication
	pubByTarget map[int64]*serverPublication
	pubGarbage  map[int64]struct{}
	pubWatchers map[*serverServiceChangeStream]struct{}

	routes map[string][]Route

	failure   error
	listenwg  sync.WaitGroup
	listeners []net.Listener
	clients   map[*serverClient]struct{}
}

// NewServer returns a new local server
func NewServer() Server {
	s := &server{
		nextTargetID: 1,

		pubByName:   make(map[string]*serverPublication),
		pubByTarget: make(map[int64]*serverPublication),
		pubGarbage:  make(map[int64]struct{}),
		pubWatchers: make(map[*serverServiceChangeStream]struct{}),

		routes: make(map[string][]Route),

		clients: make(map[*serverClient]struct{}),
	}
	return s
}

type serverPublication struct {
	owner              *server
	name               string
	targetID           int64
	localReady         map[localEndpointKey]struct{}
	localEndpoints     map[localEndpointKey]*localEndpoint
	localDistances     map[uint32]int
	localConcurrency   uint32
	localMaxQueueSize  uint32
	localMaxQueueSizes map[uint32]int
	localHasReady      sync.Cond
}

func (pub *serverPublication) selectLocalEndpoint() (*localEndpoint, error) {
	for key := range pub.localReady {
		endpoint := pub.localEndpoints[key]
		if endpoint != nil && endpoint.pub != nil && endpoint.active < endpoint.concurrency {
			endpoint.active++
			if endpoint.active == endpoint.concurrency {
				delete(pub.localReady, key)
			}
			return endpoint, nil
		}
	}
	// TODO: support for queues, but need timeouts and cancellation
	return nil, ErrOverCapacity
}

type returnableEndpoint interface {
	returnEndpointLocked() bool
}

type localEndpointKey struct {
	client   *serverClient
	targetID int64
}

type localEndpoint struct {
	pub          *serverPublication
	key          localEndpointKey
	active       uint32
	distance     uint32
	concurrency  uint32
	maxqueuesize uint32
}

func (endpoint *localEndpoint) returnEndpointLocked() bool {
	pub := endpoint.pub
	if pub == nil {
		return false
	}
	endpoint.active--
	pub.localReady[endpoint.key] = struct{}{}
	pub.localHasReady.Signal()
	return true
}

func (endpoint *localEndpoint) registerLocked(pub *serverPublication) error {
	if endpoint.pub != nil {
		return fmt.Errorf("endpoint is already published")
	}
	endpoint.pub = pub

	pub.localReady[endpoint.key] = struct{}{}
	pub.localEndpoints[endpoint.key] = endpoint
	pub.localDistances[endpoint.distance]++
	pub.localConcurrency += endpoint.concurrency
	if pub.localMaxQueueSize < endpoint.maxqueuesize {
		pub.localMaxQueueSize = endpoint.maxqueuesize
	}
	pub.localMaxQueueSizes[endpoint.maxqueuesize]++

	for watcher := range pub.owner.pubWatchers {
		watcher.addChangedLocked(pub)
	}
	return nil
}

func (endpoint *localEndpoint) unregisterLocked() error {
	pub := endpoint.pub
	if pub == nil {
		return fmt.Errorf("endpoint is not published")
	}
	endpoint.pub = nil

	newMaxQueueSizeCount := pub.localMaxQueueSizes[endpoint.maxqueuesize] - 1
	if newMaxQueueSizeCount != 0 {
		pub.localMaxQueueSizes[endpoint.maxqueuesize] = newMaxQueueSizeCount
	} else {
		delete(pub.localMaxQueueSizes, endpoint.maxqueuesize)
		pub.localMaxQueueSize = 0
		for maxqueuesize := range pub.localMaxQueueSizes {
			if pub.localMaxQueueSize < maxqueuesize {
				pub.localMaxQueueSize = maxqueuesize
			}
		}
	}
	pub.localConcurrency -= endpoint.concurrency
	newDistanceCount := pub.localDistances[endpoint.distance] - 1
	if newDistanceCount != 0 {
		pub.localDistances[endpoint.distance] = newDistanceCount
	} else {
		delete(pub.localDistances, endpoint.distance)
	}
	delete(pub.localEndpoints, endpoint.key)
	delete(pub.localReady, endpoint.key)

	if len(pub.localEndpoints) == 0 {
		pub.owner.pubGarbage[pub.targetID] = struct{}{}
		for watcher := range pub.owner.pubWatchers {
			watcher.addRemovedLocked(pub)
		}
	} else {
		for watcher := range pub.owner.pubWatchers {
			watcher.addChangedLocked(pub)
		}
	}
	return nil
}

func (s *server) publishLocalLocked(name string, key localEndpointKey, ps PublishSettings) (*localEndpoint, error) {
	pub := s.pubByName[name]
	if pub == nil {
		pub = &serverPublication{
			owner:              s,
			name:               name,
			targetID:           s.nextTargetID,
			localReady:         make(map[localEndpointKey]struct{}),
			localEndpoints:     make(map[localEndpointKey]*localEndpoint),
			localDistances:     make(map[uint32]int),
			localConcurrency:   0,
			localMaxQueueSize:  0,
			localMaxQueueSizes: make(map[uint32]int),
		}
		pub.localHasReady.L = &s.lock
		s.nextTargetID++
		s.pubByName[pub.name] = pub
		s.pubByTarget[pub.targetID] = pub
	} else {
		delete(s.pubGarbage, pub.targetID)
	}

	endpoint := &localEndpoint{
		key:          key,
		active:       0,
		distance:     ps.Distance,
		concurrency:  ps.Concurrency,
		maxqueuesize: ps.MaxQueueSize,
	}
	return endpoint, endpoint.registerLocked(pub)
}

func (s *server) failWithError(err error) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if err == nil {
		err = ErrShutdown
	}
	preverror := s.failure
	if s.failure == nil {
		s.failure = err
		listeners := s.listeners
		s.listeners = nil
		for _, listener := range listeners {
			listener.Close()
		}
		for client := range s.clients {
			delete(s.clients, client)
			client.failWithErrorLocked(err)
		}
		for cs := range s.pubWatchers {
			cs.stopLocked()
		}
	}
	return preverror
}

func (s *server) addClient(conn net.Conn) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.failure != nil {
		conn.Close()
		return s.failure
	}
	client := newServerClient(s, conn)
	s.clients[client] = struct{}{}
	return nil
}

func (s *server) acceptor(listener net.Listener) {
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			s.failWithError(err)
			return
		}
		err = s.addClient(conn)
		if err != nil {
			s.failWithError(err)
			return
		}
	}
}

func (s *server) AddPeer(network, address string, distance uint32) error {
	return ErrUnsupported
}

func (s *server) AddUpstream(network, address string) error {
	return ErrUnsupported
}

func (s *server) AddListeners(listeners ...net.Listener) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.failure != nil {
		return s.failure
	}
	for _, listener := range listeners {
		s.listeners = append(s.listeners, listener)
		s.listenwg.Add(1)
		go s.acceptor(listener)
	}
	return nil
}

func (s *server) Shutdown() error {
	return s.failWithError(ErrShutdown)
}

func (s *server) Serve() error {
	s.listenwg.Wait()
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.failure
}
