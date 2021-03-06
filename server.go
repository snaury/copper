package copper

import (
	"bufio"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type lowLevelCommon interface {
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

type lowLevelServer interface {
	lowLevelCommon
	handleNewStream(targetID int64, stream Stream) error
}

type lowLevelClient interface {
	lowLevelCommon
	openNewStream(targetID int64) (Stream, error)
}

type handleRequestStatus int

const (
	// Request was handled and the stream consumed
	handleRequestStatusDone handleRequestStatus = iota
	// There was an attempt to handle the request, but it failed early, and
	// future requests on the same path could not succeed. While the stream was
	// not consumed this status implies there was a moment with unlocked locks
	// and state might have changed. It is mandatory that something had been
	// removed before returning this status, so the request may be safely
	// retried and not loop forever.
	handleRequestStatusRetry
	// There was no route to send the request to
	handleRequestStatusNoRoute
	// There was not enough capacity to handle the request
	handleRequestStatusOverCapacity
)

type handleRequestCallback func(remote Stream) handleRequestStatus

type endpointReference interface {
	getEndpointsRLocked() []Endpoint
	handleRequestRLocked(callback handleRequestCallback, cancel <-chan struct{}) handleRequestStatus
}

type lockedRandomSource struct {
	mu  sync.Mutex
	src rand.Source
}

func (s *lockedRandomSource) Int63() (n int64) {
	s.mu.Lock()
	n = s.src.Int63()
	s.mu.Unlock()
	return n
}

func (s *lockedRandomSource) Seed(seed int64) {
	s.mu.Lock()
	s.src.Seed(seed)
	s.mu.Unlock()
}

var globalRandom = rand.New(&lockedRandomSource{
	src: rand.NewSource(time.Now().UnixNano()),
})

type server struct {
	mu sync.RWMutex

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
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
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

// Returns true if (priority, distance) is better than (minFoundPriority, minFoundDistance)
func isBetterEndpoint(priority, minFoundPriority, distance, minFoundDistance uint32) bool {
	if priority < minFoundPriority {
		// Lower priority is always better
		return true
	}
	if priority != minFoundPriority {
		// Higher priority is always worse
		return false
	}
	if minFoundDistance < 2 {
		// If minFoundDistance is already 0-1, then there is nothing better
		return false
	}
	return distance < minFoundDistance
}

// Finds the best endpoint with distance in [min, max] range.
// The rules are the following, in order:
// - Endpoints with lower priority are preferred to others
// - Endpoints with distance in [0, 1] are preferred to others (same DC)
// - Endpoints with the lowest distance >= 2 are preferred (other DCs)
func (s *server) findEndpointRLocked(name string, minDistance, maxDistance uint32) (endpoint endpointReference) {
	route := s.routeByName[name]
	if route != nil {
		return route
	}
	var minFoundPriority uint32
	var minFoundDistance uint32
	pubs := s.pubsByName[name]
	if len(pubs) > 0 && minDistance == 0 {
		for _, pub := range pubs {
			if endpoint == nil || pub.settings.Priority < minFoundPriority {
				if pub.settings.Priority == 0 {
					// Priority 0 and distance 0 is the best endpoint
					return pub
				}
				endpoint = pub
				minFoundPriority = pub.settings.Priority
				minFoundDistance = 0
			}
		}
	}
	for _, peer := range s.peers {
		if peer.distance < minDistance || peer.distance > maxDistance {
			continue
		}
		remotes := peer.remotesByName[name]
		if len(remotes) > 0 {
			for _, remote := range remotes {
				if endpoint == nil || isBetterEndpoint(remote.settings.Priority, minFoundPriority, peer.distance, minFoundDistance) {
					endpoint = remote
					minFoundPriority = remote.settings.Priority
					minFoundDistance = peer.distance
				}
			}
		}
	}
	return endpoint
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

// Extracts service name from an incoming http request
// If the request is for /{service}/path, then we re-route that request to
// locally published name http:{service} and change its path to /path.
func extractHTTPServiceName(req *http.Request) string {
	path := req.URL.Path
	if len(path) > 1 && path[0] == '/' {
		path = path[1:]
		index := strings.IndexByte(path, '/')
		if index > 0 {
			req.URL.Path = path[index:]
			return path[:index]
		}
	}
	return ""
}

func (s *server) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	status := func() handleRequestStatus {
		service := req.Header.Get("X-Copper-Service")
		if len(service) == 0 {
			handler, pattern := http.DefaultServeMux.Handler(req)
			if pattern != "" {
				// If there is a pattern match, then we have a registered
				// url at that location, use it directly.
				handler.ServeHTTP(rw, req)
				return handleRequestStatusDone
			}
			service = extractHTTPServiceName(req)
			if len(service) == 0 {
				return handleRequestStatusNoRoute
			}
		}
		minDistance := uint32(0)
		maxDistance := uint32(2)
		if value, err := strconv.Atoi(req.Header.Get("X-Copper-MinDistance")); err == nil && value >= 0 {
			minDistance = uint32(value)
		}
		if value, err := strconv.Atoi(req.Header.Get("X-Copper-MaxDistance")); err == nil && value >= 0 {
			maxDistance = uint32(value)
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		for {
			endpoint := s.findEndpointRLocked("http:"+service, minDistance, maxDistance)
			if endpoint == nil {
				return handleRequestStatusNoRoute
			}
			status := endpoint.handleRequestRLocked(func(remote Stream) handleRequestStatus {
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
					if err != nil && !isCopperError(err) {
						// Only close the stream if it was not a copper error.
						// Otherwise the remote likely closed the stream and is
						// either sending us response (that might become incomplete
						// if we reset the stream here), or it failed and reading
						// response will fail as well.
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
			}, nil)
			if status != handleRequestStatusRetry {
				return status
			}
		}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return s.failure
	}
	return s.addPeerLocked(network, address, distance)
}

func (s *server) AddListener(listener net.Listener, allowChanges bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return s.failure
	}
	s.listeners = append(s.listeners, listener)
	s.listenwg.Add(1)
	go s.acceptor(listener, allowChanges)
	return nil
}

func (s *server) AddHTTPListener(listener net.Listener) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.failure
}

func (s *server) Close() error {
	return s.closeWithError(ECONNSHUTDOWN)
}

func (s *server) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
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
