package copperd

import (
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/snaury/copper"
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
	streamServices() (ServiceChangesStream, error)
}

type endpointReference interface {
	getEndpointsLocked() []Endpoint
	handleRequestLocked(client copper.Stream) bool
}

type server struct {
	lock   sync.Mutex
	random *rand.Rand

	lastTargetID int64

	subsByName map[string]map[*serverSubscription]struct{}

	pubByTarget map[int64]*serverPublication
	pubsByName  map[string]map[uint32]*serverPublication
	pubWatchers map[*serverServiceChangesStream]struct{}

	routeByName map[string]*serverRoute

	failure   error
	listenwg  sync.WaitGroup
	listeners []net.Listener
	clients   map[*serverClient]struct{}
}

// NewServer returns a new local server
func NewServer() Server {
	s := &server{
		random: rand.New(rand.NewSource(time.Now().UnixNano())),

		subsByName: make(map[string]map[*serverSubscription]struct{}),

		pubByTarget: make(map[int64]*serverPublication),
		pubsByName:  make(map[string]map[uint32]*serverPublication),
		pubWatchers: make(map[*serverServiceChangesStream]struct{}),

		routeByName: make(map[string]*serverRoute),

		clients: make(map[*serverClient]struct{}),
	}
	return s
}

func (s *server) allocateTargetID() int64 {
	return atomic.AddInt64(&s.lastTargetID, 1)
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
