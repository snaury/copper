package copper

import (
	"bufio"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
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

func (s *server) findEndpointLocked(name string, minDistance, maxDistance uint32) (endpoint endpointReference) {
	var minPriority uint32
	route := s.routeByName[name]
	if route != nil {
		return route
	}
	pubs := s.pubsByName[name]
	if len(pubs) > 0 {
		for _, pub := range pubs {
			if pub.settings.Distance < minDistance || pub.settings.Distance > maxDistance {
				continue
			}
			if endpoint == nil || pub.settings.Priority < minPriority {
				if pub.settings.Priority == 0 {
					return pub
				}
				endpoint = pub
				minPriority = pub.settings.Priority
			}
		}
	}
	for _, peer := range s.peers {
		remotes := peer.remotesByName[name]
		if len(remotes) > 0 {
			for _, remote := range remotes {
				if remote.settings.Distance < minDistance || remote.settings.Distance > maxDistance {
					continue
				}
				if endpoint == nil || remote.settings.Priority < minPriority {
					if remote.settings.Priority == 0 {
						return remote
					}
					endpoint = remote
					minPriority = remote.settings.Priority
				}
			}
		}
	}
	return endpoint
}

func (s *server) findEndpointHTTPLocked(path string) (endpoint endpointReference) {
	if len(path) == 0 {
		path = "/"
	} else if path[0] != '/' {
		path = "/" + path
	}
	for len(path) > 0 {
		endpoint = s.findEndpointLocked("http:"+path, 0, 1)
		if endpoint != nil {
			return endpoint
		}
		endpoint = s.findEndpointLocked("http:"+path, 2, 2)
		if endpoint != nil {
			return endpoint
		}
		if path[len(path)-1] == '/' {
			path = path[:len(path)-1]
		}
		index := strings.LastIndex(path, "/")
		if index == -1 {
			break
		}
		path = path[:index+1]
	}
	return nil
}

// Hop-by-hop headers. These are removed when sent to the backend.
// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
var hopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te", // canonicalized version of "TE"
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func (s *server) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	status := func() handleRequestStatus {
		s.lock.Lock()
		defer s.lock.Unlock()
		var endpoint endpointReference
		service := req.Header.Get("X-Copper-Service")
		if len(service) != 0 {
			endpoint = s.findEndpointLocked("http:"+service, 0, 1)
			if endpoint == nil {
				endpoint = s.findEndpointLocked("http:"+service, 2, 2)
			}
		} else {
			endpoint = s.findEndpointHTTPLocked(req.URL.Path)
		}
		if endpoint == nil {
			return handleRequestStatusNoRoute
		}
		return endpoint.handleRequestLocked(func(remote Stream) handleRequestStatus {
			outreq := new(http.Request)
			*outreq = *req
			modified := false
			for _, h := range hopHeaders {
				if len(outreq.Header[h]) > 0 {
					if !modified {
						outreq.Header = make(http.Header)
						copyHeaders(outreq.Header, req.Header)
					}
					delete(outreq.Header, h)
				}
			}
			go func() {
				err := outreq.Write(remote)
				if err != nil {
					remote.CloseWithError(err)
				}
			}()
			r := bufio.NewReader(remote)
			res, err := http.ReadResponse(r, outreq)
			if err == nil && res.StatusCode == 100 {
				// Skip 100-continue
				res, err = http.ReadResponse(r, outreq)
			}
			if err != nil {
				remote.CloseWithError(err)
				text := err.Error()
				rw.Header().Set("Content-Length", fmt.Sprintf("%d", len(text)))
				rw.WriteHeader(502)
				rw.Write([]byte(text))
			} else {
				defer res.Body.Close()
				copyHeaders(rw.Header(), res.Header)
				rw.WriteHeader(res.StatusCode)
				io.Copy(rw, res.Body)
			}
			return handleRequestStatusDone
		})
	}()
	if status != handleRequestStatusDone {
		rw.WriteHeader(404)
	}
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
