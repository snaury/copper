package copperd

import (
	"fmt"
	"net"
	"sync"
)

type lowLevelServer interface {
	subscribe(options ...SubscribeOption) (int64, error)
	getEndpoints(targetID int64) ([]Endpoint, error)
	streamEndpoints(targetID int64) (EndpointChangesStream, error)
	unsubscribe(targetID int64) error
	publish(targetID int64, name string, settings PublishSettings) error
	unpublish(targetID int64) error
	setRoute(name string, routes ...Route) error
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
	name              string
	targetID          int64
	localConcurrency  uint32
	localDistances    map[uint32]int
	localEndpoints    map[*serverClient]map[int64]PublishSettings
	remoteConcurrency uint32
	remoteDistances   map[uint32]int
	remoteEndpoints   map[Endpoint]PublishSettings
}

func (s *server) publishLocalLocked(name string, c *serverClient, targetID int64, settings PublishSettings) error {
	pub := s.pubByName[name]
	if pub == nil {
		pub = &serverPublication{
			name:              name,
			targetID:          s.nextTargetID,
			localConcurrency:  0,
			localEndpoints:    make(map[*serverClient]map[int64]PublishSettings),
			localDistances:    make(map[uint32]int),
			remoteConcurrency: 0,
			remoteDistances:   make(map[uint32]int),
			remoteEndpoints:   make(map[Endpoint]PublishSettings),
		}
		s.nextTargetID++
		s.pubByName[pub.name] = pub
		s.pubByTarget[pub.targetID] = pub
	} else {
		delete(s.pubGarbage, pub.targetID)
	}

	endpoints := pub.localEndpoints[c]
	if endpoints == nil {
		endpoints = make(map[int64]PublishSettings)
		pub.localEndpoints[c] = endpoints
	}
	endpoints[targetID] = settings

	pub.localDistances[settings.Distance]++
	pub.localConcurrency += settings.Concurrency
	for watcher := range s.pubWatchers {
		watcher.addChangedLocked(pub)
	}

	return nil
}

func (s *server) unpublishLocalLocked(name string, c *serverClient, targetID int64) error {
	pub := s.pubByName[name]
	if pub == nil {
		return fmt.Errorf("name %q is not published", name)
	}
	endpoints := pub.localEndpoints[c]
	if endpoints == nil {
		return fmt.Errorf("name %q has no endpoints from this client", name)
	}
	settings, ok := endpoints[targetID]
	if !ok {
		return fmt.Errorf("name %q has no endpoint from this client with target %d", name, targetID)
	}

	delete(endpoints, targetID)
	if len(endpoints) == 0 {
		delete(pub.localEndpoints, c)
	}

	pub.localConcurrency -= settings.Concurrency
	newCount := pub.localDistances[settings.Distance] - 1
	if newCount != 0 {
		pub.localDistances[settings.Distance] = newCount
	} else {
		delete(pub.localDistances, settings.Distance)
	}

	if len(pub.localEndpoints) == 0 && len(pub.remoteEndpoints) == 0 {
		s.pubGarbage[pub.targetID] = struct{}{}
		for watcher := range s.pubWatchers {
			watcher.addRemovedLocked(pub)
		}
	} else {
		for watcher := range s.pubWatchers {
			if len(pub.localEndpoints) == 0 {
				watcher.addRemovedLocked(pub)
			} else {
				watcher.addChangedLocked(pub)
			}
		}
	}

	return nil
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

func (s *server) Close() error {
	return s.failWithError(ErrShutdown)
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
