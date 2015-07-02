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
		for sub := range s.subByName[pub.name] {
			sub.addPublicationLocked(pub)
		}
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
