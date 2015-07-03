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

func (endpoint *localEndpoint) getEndpointsLocked() []Endpoint {
	return nil
}

func (endpoint *localEndpoint) handleRequestLocked(client copper.Stream) bool {
	if endpoint.pub != nil && endpoint.active < endpoint.settings.Concurrency {
		return endpoint.passthruRequestLocked(client)
	}
	return false
}

func (endpoint *localEndpoint) passthruRequestLocked(client copper.Stream) bool {
	endpoint.active++
	if endpoint.active == endpoint.settings.Concurrency {
		delete(endpoint.pub.ready, endpoint)
	}
	defer func() {
		if endpoint.pub != nil {
			endpoint.active--
			endpoint.pub.ready[endpoint] = struct{}{}
			endpoint.pub.wakeupWaitersLocked(1)
		}
	}()
	endpoint.owner.lock.Unlock()
	defer endpoint.owner.lock.Lock()
	remote, err := endpoint.key.client.conn.Open(endpoint.key.targetID)
	if err != nil {
		// this client has already disconnected
		return false
	}
	passthruBoth(client, remote)
	return true
}

type serverPublication struct {
	owner    *server
	name     string
	targetID int64

	endpoints     map[localEndpointKey]*localEndpoint
	distances     map[uint32]int
	maxqueuesizes map[uint32]int
	settings      PublishSettings

	ready map[*localEndpoint]struct{}
	queue map[*sync.Cond]struct{}

	subscriptions map[*serverSubscription]struct{}
}

var _ endpointReference = &serverPublication{}

func (pub *serverPublication) waitInQueueLocked(waiter *sync.Cond) {
	pub.queue[waiter] = struct{}{}
	waiter.Wait()
	delete(pub.queue, waiter)
}

func (pub *serverPublication) wakeupWaitersLocked(n int) {
	for waiter := range pub.queue {
		delete(pub.queue, waiter)
		waiter.Signal()
		n--
		if n == 0 {
			break
		}
	}
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

func (pub *serverPublication) handleRequestLocked(client copper.Stream) bool {
	var waiter *sync.Cond
	for len(pub.endpoints) > 0 {
		for endpoint := range pub.ready {
			if endpoint.handleRequestLocked(client) {
				return true
			}
		}
		if len(pub.queue) >= int(pub.settings.MaxQueueSize) {
			// Queue for this publication is already full
			client.CloseWithError(ErrOverCapacity)
			return true
		}
		if waiter == nil {
			// TODO: support for timeout and cancellation
			waiter = sync.NewCond(&pub.owner.lock)
		}
		pub.waitInQueueLocked(waiter)
	}
	return false
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
			queue: make(map[*sync.Cond]struct{}),

			subscriptions: make(map[*serverSubscription]struct{}),
		}
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
	pub.wakeupWaitersLocked(int(endpoint.settings.Concurrency))

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

	pub.wakeupWaitersLocked(len(pub.queue))
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
