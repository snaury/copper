package copperd

import (
	"fmt"
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
	streamServices() (ServiceChangeStream, error)
}

type server struct {
	lock   sync.Mutex
	random *rand.Rand

	lastTargetID int64

	subByTarget map[int64]*serverSubscription
	subByName   map[string]map[*serverSubscription]struct{}

	pubByTarget map[int64]*serverPublication
	pubByName   map[string]*serverPublication
	pubWatchers map[*serverServiceChangeStream]struct{}

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

		subByTarget: make(map[int64]*serverSubscription),
		subByName:   make(map[string]map[*serverSubscription]struct{}),

		pubByTarget: make(map[int64]*serverPublication),
		pubByName:   make(map[string]*serverPublication),
		pubWatchers: make(map[*serverServiceChangeStream]struct{}),

		routeByName: make(map[string]*serverRoute),

		clients: make(map[*serverClient]struct{}),
	}
	return s
}

func (s *server) allocateTargetID() int64 {
	return atomic.AddInt64(&s.lastTargetID, 1)
}

type endpointReference interface {
	open() (copper.Stream, error)
	decref() bool
	getEndpointsLocked() []Endpoint
	selectEndpointLocked() (endpointReference, error)
}

type serverSubscription struct {
	owner    *server
	targetID int64
	settings SubscribeSettings

	routes []*serverRoute
	locals []*serverPublication
	active int
}

var _ endpointReference = &serverSubscription{}

func (sub *serverSubscription) open() (copper.Stream, error) {
	return nil, ErrUnsupported
}

func (sub *serverSubscription) decref() bool {
	return false
}

func (sub *serverSubscription) getEndpointsLocked() []Endpoint {
	if sub.active < len(sub.settings.Options) {
		if route := sub.routes[sub.active]; route != nil {
			return route.getEndpointsLocked()
		}
		if local := sub.locals[sub.active]; local != nil {
			return local.getEndpointsLocked()
		}
		// TODO: support remote services
	}
	// TODO: support upstream
	return nil
}

func (sub *serverSubscription) selectEndpointLocked() (endpointReference, error) {
	if sub.active < len(sub.settings.Options) {
		if route := sub.routes[sub.active]; route != nil {
			return route.selectEndpointLocked()
		}
		if local := sub.locals[sub.active]; local != nil {
			return local.selectEndpointLocked()
		}
		// TODO: support remote services
	}
	// TODO: support upstream
	return nil, copper.ENOROUTE
}

func (sub *serverSubscription) isActiveLocked() bool {
	return sub.active < len(sub.settings.Options)
}

func (sub *serverSubscription) updateActiveIndexLocked() {
	sub.active = len(sub.settings.Options)
	for index := range sub.settings.Options {
		if sub.routes[index] != nil || sub.locals[index] != nil {
			if sub.active > index {
				sub.active = index
				break
			}
		}
		// TODO: support remote services
	}
}

func (sub *serverSubscription) addRouteLocked(route *serverRoute) {
	if sub.settings.DisableRoutes {
		return
	}
	changed := false
	for index, option := range sub.settings.Options {
		if option.Service == route.name {
			route.subscriptions[sub] = struct{}{}
			sub.routes[index] = route
			changed = true
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) removeRouteLocked(route *serverRoute) {
	if sub.settings.DisableRoutes {
		return
	}
	changed := false
	for index := range sub.settings.Options {
		if sub.routes[index] == route {
			sub.routes[index] = nil
			changed = true
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) addPublicationLocked(pub *serverPublication) {
	changed := false
	for index, option := range sub.settings.Options {
		if option.MinDistance == 0 && option.Service == pub.name {
			// This option allows local services and matches publication name
			pub.subscriptions[sub] = struct{}{}
			sub.locals[index] = pub
			changed = true
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) removePublicationLocked(pub *serverPublication) {
	changed := false
	for index := range sub.settings.Options {
		if sub.locals[index] == pub {
			sub.locals[index] = nil
			changed = true
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) registerLocked() {
	changed := false
	sub.owner.subByTarget[sub.targetID] = sub
	for index, option := range sub.settings.Options {
		// Let others know we have an interested in this name
		subs := sub.owner.subByName[option.Service]
		if subs == nil {
			subs = make(map[*serverSubscription]struct{})
			sub.owner.subByName[option.Service] = subs
		}
		subs[sub] = struct{}{}
		if !sub.settings.DisableRoutes {
			route := sub.owner.routeByName[option.Service]
			if route != nil {
				sub.routes[index] = route
				route.subscriptions[sub] = struct{}{}
			}
		}
		if option.MinDistance == 0 {
			// This option allows local services, look them up
			pub := sub.owner.pubByName[option.Service]
			if pub != nil {
				pub.subscriptions[sub] = struct{}{}
				sub.locals[index] = pub
				changed = true
			}
		}
		// TODO: support remote services
	}
	// TODO: support upstream
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) unregisterLocked() {
	// TODO: support upstream
	for index, option := range sub.settings.Options {
		// TODO: support remote services
		if pub := sub.locals[index]; pub != nil {
			sub.locals[index] = nil
			delete(pub.subscriptions, sub)
		}
		if route := sub.routes[index]; route != nil {
			sub.routes[index] = nil
			delete(route.subscriptions, sub)
		}
		if subs := sub.owner.subByName[option.Service]; subs != nil {
			delete(subs, sub)
			if len(subs) == 0 {
				delete(sub.owner.subByName, option.Service)
			}
		}
	}
	delete(sub.owner.subByTarget, sub.targetID)
	sub.active = len(sub.settings.Options)
}

func (s *server) subscribeLocked(settings SubscribeSettings) (*serverSubscription, error) {
	if len(settings.Options) == 0 {
		return nil, fmt.Errorf("cannot subscribe with 0 options")
	}
	sub := &serverSubscription{
		owner:    s,
		targetID: s.allocateTargetID(),
		settings: settings,

		routes: make([]*serverRoute, len(settings.Options)),
		locals: make([]*serverPublication, len(settings.Options)),
		active: len(settings.Options),
	}
	sub.registerLocked()
	return sub, nil
}

type serverPublication struct {
	owner    *server
	name     string
	targetID int64

	endpoints     map[localEndpointKey]*localEndpoint
	distances     map[uint32]int
	concurrency   uint32
	maxqueuesize  uint32
	maxqueuesizes map[uint32]int
	ready         map[*localEndpoint]struct{}
	hasready      sync.Cond

	subscriptions map[*serverSubscription]struct{}
}

var _ endpointReference = &serverPublication{}

func (pub *serverPublication) open() (copper.Stream, error) {
	return nil, ErrUnsupported
}

func (pub *serverPublication) decref() bool {
	return false
}

func (pub *serverPublication) getEndpointsLocked() []Endpoint {
	return []Endpoint{
		Endpoint{
			Network:  "",
			Address:  "",
			TargetID: pub.targetID,
		},
	}
}

func (pub *serverPublication) selectEndpointLocked() (endpointReference, error) {
	if len(pub.endpoints) == 0 {
		return nil, copper.ENOROUTE
	}
	for endpoint := range pub.ready {
		if target, err := endpoint.selectEndpointLocked(); err == nil {
			return target, nil
		}
	}
	// TODO: support for queues, but need timeouts and cancellation
	return nil, ErrOverCapacity
}

type localEndpointKey struct {
	client   *serverClient
	targetID int64
}

type localEndpoint struct {
	owner        *server
	pub          *serverPublication
	key          localEndpointKey
	active       uint32
	distance     uint32
	concurrency  uint32
	maxqueuesize uint32
}

var _ endpointReference = &localEndpoint{}

func (endpoint *localEndpoint) open() (copper.Stream, error) {
	return endpoint.key.client.conn.Open(endpoint.key.targetID)
}

func (endpoint *localEndpoint) decref() bool {
	endpoint.owner.lock.Lock()
	defer endpoint.owner.lock.Unlock()
	pub := endpoint.pub
	if pub == nil {
		return false
	}
	endpoint.active--
	pub.ready[endpoint] = struct{}{}
	pub.hasready.Signal()
	return true
}

func (endpoint *localEndpoint) getEndpointsLocked() []Endpoint {
	return nil
}

func (endpoint *localEndpoint) selectEndpointLocked() (endpointReference, error) {
	if endpoint.pub != nil && endpoint.active < endpoint.concurrency {
		endpoint.active++
		if endpoint.active == endpoint.concurrency {
			delete(endpoint.pub.ready, endpoint)
		}
		return endpoint, nil
	}
	return nil, ErrOverCapacity
}

func (endpoint *localEndpoint) registerLocked(pub *serverPublication) error {
	if endpoint.pub != nil {
		return fmt.Errorf("endpoint is already published")
	}
	endpoint.pub = pub

	pub.endpoints[endpoint.key] = endpoint
	pub.distances[endpoint.distance]++
	pub.concurrency += endpoint.concurrency
	if pub.maxqueuesize < endpoint.maxqueuesize {
		pub.maxqueuesize = endpoint.maxqueuesize
	}
	pub.maxqueuesizes[endpoint.maxqueuesize]++
	pub.ready[endpoint] = struct{}{}
	pub.hasready.Broadcast()

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

	delete(pub.ready, endpoint)
	newMaxQueueSizeCount := pub.maxqueuesizes[endpoint.maxqueuesize] - 1
	if newMaxQueueSizeCount != 0 {
		pub.maxqueuesizes[endpoint.maxqueuesize] = newMaxQueueSizeCount
	} else {
		delete(pub.maxqueuesizes, endpoint.maxqueuesize)
		pub.maxqueuesize = 0
		for maxqueuesize := range pub.maxqueuesizes {
			if pub.maxqueuesize < maxqueuesize {
				pub.maxqueuesize = maxqueuesize
			}
		}
	}
	pub.concurrency -= endpoint.concurrency
	newDistanceCount := pub.distances[endpoint.distance] - 1
	if newDistanceCount != 0 {
		pub.distances[endpoint.distance] = newDistanceCount
	} else {
		delete(pub.distances, endpoint.distance)
	}
	delete(pub.endpoints, endpoint.key)

	if len(pub.endpoints) == 0 {
		delete(pub.owner.pubByName, pub.name)
		delete(pub.owner.pubByTarget, pub.targetID)
		for watcher := range pub.owner.pubWatchers {
			watcher.addRemovedLocked(pub)
		}
		// TODO: notify subscriptions
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
			owner:    s,
			name:     name,
			targetID: s.allocateTargetID(),

			endpoints:     make(map[localEndpointKey]*localEndpoint),
			distances:     make(map[uint32]int),
			concurrency:   0,
			maxqueuesize:  0,
			maxqueuesizes: make(map[uint32]int),

			ready: make(map[*localEndpoint]struct{}),

			subscriptions: make(map[*serverSubscription]struct{}),
		}
		pub.hasready.L = &s.lock
		s.pubByName[pub.name] = pub
		s.pubByTarget[pub.targetID] = pub
	}

	endpoint := &localEndpoint{
		owner:        s,
		key:          key,
		active:       0,
		distance:     ps.Distance,
		concurrency:  ps.Concurrency,
		maxqueuesize: ps.MaxQueueSize,
	}
	return endpoint, endpoint.registerLocked(pub)
}

type serverRouteCase struct {
	sub    *serverSubscription
	weight uint32
}

type serverRoute struct {
	owner  *server
	name   string
	routes []Route

	cases []serverRouteCase

	subscriptions map[*serverSubscription]struct{}
}

var _ endpointReference = &serverRoute{}

func (r *serverRoute) open() (copper.Stream, error) {
	return nil, ErrUnsupported
}

func (r *serverRoute) decref() bool {
	return false
}

func (r *serverRoute) getEndpointsLocked() []Endpoint {
	var result []Endpoint
	for _, c := range r.cases {
		if c.weight > 0 && c.sub.isActiveLocked() {
			result = append(result, c.sub.getEndpointsLocked()...)
		}
	}
	return result
}

func (r *serverRoute) selectEndpointLocked() (endpointReference, error) {
	sum := int64(0)
	for _, c := range r.cases {
		if c.weight > 0 && c.sub.isActiveLocked() {
			sum += int64(c.weight)
		}
	}
	if sum == 0 {
		// There are no routes
		return nil, copper.ENOROUTE
	}
	bin := r.owner.random.Int63n(sum)
	for _, c := range r.cases {
		if c.weight > 0 && c.sub.isActiveLocked() {
			if bin < int64(c.weight) {
				return c.sub.selectEndpointLocked()
			}
			bin -= int64(c.weight)
		}
	}
	// this should never happen, might replace it with a panic!
	return nil, fmt.Errorf("random number %d didn't match the %d sum", bin, sum)
}

func (s *server) setRouteLocked(name string, routes []Route) error {
	r := s.routeByName[name]
	if r == nil {
		if len(routes) == 0 {
			return nil
		}
		r = &serverRoute{
			owner:  s,
			name:   name,
			routes: routes,

			subscriptions: make(map[*serverSubscription]struct{}),
		}
		s.routeByName[name] = r
		if subs := s.subByName[name]; subs != nil {
			for sub := range subs {
				sub.addRouteLocked(r)
			}
		}
	} else if len(routes) == 0 {
		for sub := range r.subscriptions {
			sub.removeRouteLocked(r)
		}
		r.subscriptions = nil
		for _, c := range r.cases {
			c.sub.unregisterLocked()
		}
		r.cases = nil
		r.routes = nil
		delete(s.routeByName, name)
		return nil
	} else {
		for _, c := range r.cases {
			c.sub.unregisterLocked()
		}
		r.cases = nil
		r.routes = routes
	}
	// we need to build our cases
	r.cases = make([]serverRouteCase, len(r.routes))
	for index, route := range r.routes {
		sub, err := s.subscribeLocked(SubscribeSettings{
			Options:       route.Options,
			MaxRetries:    1,
			DisableRoutes: true,
		})
		if err != nil {
			panic(fmt.Errorf("unexpected subscribe error: %s", err))
		}
		r.cases[index].sub = sub
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
