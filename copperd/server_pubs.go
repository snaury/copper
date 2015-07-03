package copperd

import (
	"fmt"
	"sync"

	"github.com/snaury/copper"
)

type localEndpointKey struct {
	client   *serverClient
	targetID int64
}

type localEndpoint struct {
	owner    *server
	pub      *serverPublication
	key      localEndpointKey
	active   uint32
	settings PublishSettings
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
	if endpoint.pub != nil && endpoint.active < endpoint.settings.Concurrency {
		endpoint.active++
		if endpoint.active == endpoint.settings.Concurrency {
			delete(endpoint.pub.ready, endpoint)
		}
		return endpoint, nil
	}
	return nil, ErrOverCapacity
}

type serverPublication struct {
	owner    *server
	name     string
	targetID int64

	endpoints     map[localEndpointKey]*localEndpoint
	distances     map[uint32]int
	maxqueuesizes map[uint32]int
	settings      PublishSettings

	ready    map[*localEndpoint]struct{}
	hasready sync.Cond

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

func (s *server) publishLocked(name string, key localEndpointKey, settings PublishSettings) (*localEndpoint, error) {
	pubs := s.pubsByName[name]
	if pubs == nil {
		pubs = make(map[uint32]*serverPublication)
		s.pubsByName[name] = pubs
	}

	pub := pubs[settings.Priority]
	if pub == nil {
		pub = &serverPublication{
			owner:    s,
			name:     name,
			targetID: s.allocateTargetID(),

			endpoints:     make(map[localEndpointKey]*localEndpoint),
			distances:     make(map[uint32]int),
			maxqueuesizes: make(map[uint32]int),
			settings:      settings,

			ready: make(map[*localEndpoint]struct{}),

			subscriptions: make(map[*serverSubscription]struct{}),
		}
		pub.hasready.L = &s.lock
		pubs[settings.Priority] = pub
		s.pubByTarget[pub.targetID] = pub
		for sub := range s.subsByName[pub.name] {
			sub.addPublicationLocked(pub)
		}
	}

	if pub.endpoints[key] != nil {
		return nil, fmt.Errorf("endpoint with key [target=%d] is already published", key.targetID)
	}

	endpoint := &localEndpoint{
		owner:    s,
		pub:      pub,
		key:      key,
		active:   0,
		settings: settings,
	}

	pub.endpoints[endpoint.key] = endpoint
	if len(pub.endpoints) != 1 {
		if pub.settings.Distance < endpoint.settings.Distance {
			pub.settings.Distance = endpoint.settings.Distance
		}
		pub.settings.Concurrency += endpoint.settings.Concurrency
		if pub.settings.MaxQueueSize < endpoint.settings.MaxQueueSize {
			pub.settings.MaxQueueSize = endpoint.settings.MaxQueueSize
		}
	}
	pub.distances[endpoint.settings.Distance]++
	pub.maxqueuesizes[endpoint.settings.MaxQueueSize]++
	pub.ready[endpoint] = struct{}{}
	pub.hasready.Broadcast()

	for watcher := range pub.owner.pubWatchers {
		watcher.addChangedLocked(pub)
	}
	return endpoint, nil
}

func (endpoint *localEndpoint) unpublishLocked() error {
	pub := endpoint.pub
	if pub == nil {
		return fmt.Errorf("endpoint is not published")
	}
	endpoint.pub = nil

	delete(pub.ready, endpoint)
	if decrementCounterUint32(pub.distances, endpoint.settings.Distance) {
		pub.settings.Distance = 0
		for distance := range pub.distances {
			if pub.settings.Distance < distance {
				pub.settings.Distance = distance
			}
		}
	}
	pub.settings.Concurrency -= endpoint.settings.Concurrency
	if decrementCounterUint32(pub.maxqueuesizes, endpoint.settings.MaxQueueSize) {
		pub.settings.MaxQueueSize = 0
		for maxqueuesize := range pub.maxqueuesizes {
			if pub.settings.MaxQueueSize < maxqueuesize {
				pub.settings.MaxQueueSize = maxqueuesize
			}
		}
	}
	delete(pub.endpoints, endpoint.key)

	if len(pub.endpoints) == 0 {
		delete(pub.owner.pubByTarget, pub.targetID)
		if pubs := pub.owner.pubsByName[pub.name]; pubs != nil {
			delete(pubs, pub.settings.Priority)
			if len(pubs) == 0 {
				delete(pub.owner.pubsByName, pub.name)
			}
		}
		for watcher := range pub.owner.pubWatchers {
			watcher.addRemovedLocked(pub)
		}
		for sub := range pub.subscriptions {
			sub.removePublicationLocked(pub)
		}
	} else {
		for watcher := range pub.owner.pubWatchers {
			watcher.addChangedLocked(pub)
		}
	}
	return nil
}
