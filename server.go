package copper

import (
	"math/rand"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
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

type handleRequestStatus int

const (
	// Request was handled and the stream consumed
	handleRequestStatusDone handleRequestStatus = iota
	// There was an attempt to handle the request, but it was impossible, and
	// future requests are unlikely to succeed. While the stream was not
	// consumed this status implies that locks had been unlocked and
	// configuration has changed.
	handleRequestStatusImpossible
	// There was not enough capacity to handle the request
	handleRequestStatusOverCapacity
	// There was no route to send the request to
	handleRequestStatusNoRoute
)

type handleRequestCallback func(remote Stream) handleRequestStatus

type endpointReference interface {
	getEndpointsLocked() []Endpoint
	handleRequestLocked(callback handleRequestCallback) handleRequestStatus
}

type server struct {
	lock   sync.Mutex
	random *rand.Rand

	lastTargetID int64

	peers map[serverPeerKey]*serverPeer

	subsByName map[string]map[*serverSubscription]struct{}

	pubByTarget map[int64]*serverPublication
	pubsByName  map[string]map[uint32]*serverPublication
	pubWatchers map[*serverServiceChangesStream]struct{}

	routeByName map[string]*serverRoute

	failure    error
	closedchan chan struct{}
	finishchan chan struct{}
	listenwg   sync.WaitGroup
	clientwg   sync.WaitGroup
	listeners  []net.Listener

	clients map[*serverClient]struct{}
}

// NewServer returns a new local server
func NewServer() Server {
	s := &server{
		random: rand.New(rand.NewSource(time.Now().UnixNano())),

		peers: make(map[serverPeerKey]*serverPeer),

		subsByName: make(map[string]map[*serverSubscription]struct{}),

		pubByTarget: make(map[int64]*serverPublication),
		pubsByName:  make(map[string]map[uint32]*serverPublication),
		pubWatchers: make(map[*serverServiceChangesStream]struct{}),

		routeByName: make(map[string]*serverRoute),

		closedchan: make(chan struct{}),

		clients: make(map[*serverClient]struct{}),
	}
	return s
}

func (s *server) allocateTargetID() int64 {
	return atomic.AddInt64(&s.lastTargetID, 1)
}

func (s *server) closeWithError(err error) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if err == nil {
		err = ECONNCLOSED
	}
	preverror := s.failure
	if s.failure == nil {
		s.failure = err
		listeners := s.listeners
		s.listeners = nil
		for _, listener := range listeners {
			listener.Close()
		}
		for _, peer := range s.peers {
			peer.closeWithErrorLocked(err)
		}
		for client := range s.clients {
			client.closeWithErrorLocked(err)
		}
		for cs := range s.pubWatchers {
			cs.stopLocked()
		}
	}
	return preverror
}

func (s *server) addClient(conn net.Conn, allowChanges bool) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.failure != nil {
		conn.Close()
		return s.failure
	}
	client := newServerClient(s, conn, allowChanges)
	s.clients[client] = struct{}{}
	return nil
}

func (s *server) acceptor(listener net.Listener, allowChanges bool) {
	defer s.listenwg.Done()
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			s.closeWithError(err)
			return
		}
		err = s.addClient(conn, allowChanges)
		if err != nil {
			s.closeWithError(err)
			return
		}
	}
}

func (s *server) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// TODO
	// Find where to forward this request, using the longest part of the uri.
	// First routes should be checked, then local/remote depending on priority
}

func (s *server) httpacceptor(listener net.Listener) {
	defer s.listenwg.Done()
	defer listener.Close()
	httpserver := &http.Server{
		Handler: s,
	}
	err := httpserver.Serve(listener)
	if err != nil {
		s.closeWithError(err)
		return
	}
}

func (s *server) AddPeer(network, address string, distance uint32) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.failure != nil {
		return s.failure
	}
	return s.addPeerLocked(network, address, distance)
}

func (s *server) AddListener(listener net.Listener, allowChanges bool) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.failure != nil {
		return s.failure
	}
	s.listeners = append(s.listeners, listener)
	s.listenwg.Add(1)
	go s.acceptor(listener, allowChanges)
	return nil
}

func (s *server) AddHTTPListener(listener net.Listener) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.failure != nil {
		return s.failure
	}
	s.listeners = append(s.listeners, listener)
	s.listenwg.Add(1)
	go s.httpacceptor(listener)
	return nil
}

func (s *server) SetUpstream(upstream Client) error {
	return EUNSUPPORTED
}

func (s *server) Err() error {
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.failure
}

func (s *server) Close() error {
	return s.closeWithError(ECONNSHUTDOWN)
}

func (s *server) Done() <-chan struct{} {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.finishchan == nil {
		s.finishchan = make(chan struct{})
		go func() {
			defer close(s.finishchan)
			s.listenwg.Wait()
			s.clientwg.Wait()
		}()
	}
	return s.finishchan
}

func (s *server) Closed() <-chan struct{} {
	return s.closedchan
}
