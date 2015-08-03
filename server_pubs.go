package copper

import (
	"fmt"
	"sync"
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

func (endpoint *localEndpoint) getEndpointsRLocked() []Endpoint {
	return nil
}

func (endpoint *localEndpoint) handleRequestRLocked(callback handleRequestCallback) handleRequestStatus {
	endpoint.owner.mu.RUnlock()
	defer endpoint.owner.mu.RLock()
	remote, err := rpcNewStream(endpoint.key.client.conn, endpoint.key.targetID)
	if err != nil {
		// this client has already disconnected
		return handleRequestStatusImpossible
	}
	defer remote.Close()
	return callback(remote)
}

type serverPublication struct {
	mu       sync.Mutex
	owner    *server
	name     string
	targetID int64

	endpoints map[localEndpointKey]*localEndpoint
	distances map[uint32]int
	settings  PublishSettings

	ready map[*localEndpoint]struct{}
	queue []chan struct{}

	subscriptions map[*serverSubscription]struct{}
}

var _ endpointReference = &serverPublication{}

func (pub *serverPublication) takeEndpointPubLocked() *localEndpoint {
	for endpoint := range pub.ready {
		endpoint.active++
		if endpoint.active == endpoint.settings.Concurrency {
			delete(pub.ready, endpoint)
		}
		return endpoint
	}
	return nil
}

func (pub *serverPublication) releaseEndpointPubLocked(endpoint *localEndpoint) {
	if endpoint.pub == pub {
		endpoint.active--
		endpoint.pub.ready[endpoint] = struct{}{}
		endpoint.pub.wakeupWaitersPubLocked(1)
	}
}

func (pub *serverPublication) waitInQueuePubLocked() {
	waiter := make(chan struct{})
	pub.queue = append(pub.queue, waiter)
	pub.mu.Unlock()
	pub.owner.mu.RUnlock()
	<-waiter
	pub.owner.mu.RLock()
	pub.mu.Lock()
}

func (pub *serverPublication) wakeupWaitersPubLocked(n int) {
	if n > len(pub.queue) {
		n = len(pub.queue)
	}
	if n > 0 {
		for i := 0; i < n; i++ {
			close(pub.queue[i])
		}
		live := copy(pub.queue, pub.queue[n:])
		for i := live; i < len(pub.queue); i++ {
			pub.queue[i] = nil
		}
		pub.queue = pub.queue[:live]
	}
}

func (pub *serverPublication) getEndpointsRLocked() []Endpoint {
	return []Endpoint{
		Endpoint{
			Network:  "",
			Address:  "",
			TargetID: pub.targetID,
		},
	}
}

func (pub *serverPublication) handleRequestRLocked(callback handleRequestCallback) handleRequestStatus {
	pub.mu.Lock()
	for len(pub.endpoints) > 0 {
		endpoint := pub.takeEndpointPubLocked()
		if endpoint != nil {
			pub.mu.Unlock()
			status := endpoint.handleRequestRLocked(callback)
			pub.mu.Lock()
			pub.releaseEndpointPubLocked(endpoint)
			// FIXME: process handleRequestStatusImpossible?
			pub.mu.Unlock()
			return status
		}
		if len(pub.queue) >= int(pub.settings.QueueSize) {
			// Queue for this publication is already full
			pub.mu.Unlock()
			return handleRequestStatusOverCapacity
		}
		pub.waitInQueuePubLocked()
	}
	pub.mu.Unlock()
	return handleRequestStatusNoRoute
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

			endpoints: make(map[localEndpointKey]*localEndpoint),
			distances: make(map[uint32]int),
			settings:  settings,

			ready: make(map[*localEndpoint]struct{}),

			subscriptions: make(map[*serverSubscription]struct{}),
		}
		pubs[settings.Priority] = pub
		s.pubByTarget[pub.targetID] = pub
		for sub := range s.subsByName[pub.name] {
			sub.addPublicationLocked(pub)
		}
	} else {
		if pub.endpoints[key] != nil {
			return nil, fmt.Errorf("new endpoint with target=%d conflicts with a previous publication", key.targetID)
		}
		if pub.settings.Concurrency+settings.Concurrency < pub.settings.Concurrency {
			return nil, fmt.Errorf("new endpoint with target=%d overflows maximum capacity", key.targetID)
		}
		if pub.settings.QueueSize+settings.QueueSize < pub.settings.QueueSize {
			return nil, fmt.Errorf("new endpoint with target=%d overflows maximum queue size", key.targetID)
		}
		if pub.settings.Distance < settings.Distance {
			pub.settings.Distance = settings.Distance
		}
		pub.settings.Concurrency += settings.Concurrency
		pub.settings.QueueSize += settings.QueueSize
	}
	pub.distances[settings.Distance]++

	endpoint := &localEndpoint{
		owner:    s,
		pub:      pub,
		key:      key,
		active:   0,
		settings: settings,
	}

	pub.endpoints[endpoint.key] = endpoint
	pub.ready[endpoint] = struct{}{}

	pub.wakeupWaitersPubLocked(int(settings.Concurrency))

	for watcher := range pub.owner.pubWatchers {
		watcher.addChangedLocked(pub)
	}

	if log := DebugLog(); log != nil {
		log.Printf("Service %s(priority=%d): published locally (total: endpoints=%d, concurrency=%d, queue_size=%d, max_distance=%d)",
			pub.name, pub.settings.Priority, len(pub.endpoints), pub.settings.Concurrency, pub.settings.QueueSize, pub.settings.Distance)
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
	pub.settings.QueueSize -= endpoint.settings.QueueSize
	delete(pub.endpoints, endpoint.key)

	if len(pub.endpoints) == 0 {
		delete(pub.owner.pubByTarget, pub.targetID)
		if pubs := pub.owner.pubsByName[pub.name]; pubs != nil {
			delete(pubs, pub.settings.Priority)
			if len(pubs) == 0 {
				delete(pub.owner.pubsByName, pub.name)
			}
		}
		for _, waiter := range pub.queue {
			close(waiter)
		}
		pub.queue = nil
		for watcher := range pub.owner.pubWatchers {
			watcher.addRemovedLocked(pub)
		}
		for sub := range pub.subscriptions {
			sub.removePublicationLocked(pub)
		}
		if log := DebugLog(); log != nil {
			log.Printf("Service %s(priority=%d): unpublished locally (no endpoints left)",
				pub.name, pub.settings.Priority)
		}
	} else {
		if len(pub.queue) > int(pub.settings.QueueSize) {
			for i := int(pub.settings.QueueSize); i < len(pub.queue); i++ {
				close(pub.queue[i])
				pub.queue[i] = nil
			}
			pub.queue = pub.queue[:int(pub.settings.QueueSize)]
		}
		for watcher := range pub.owner.pubWatchers {
			watcher.addChangedLocked(pub)
		}
		if log := DebugLog(); log != nil {
			log.Printf("Service %s(priority=%d): unpublished locally (total: endpoints=%d, concurrency=%d, queue_size=%d, max_distance=%d)",
				pub.name, pub.settings.Priority, len(pub.endpoints), pub.settings.Concurrency, pub.settings.QueueSize, pub.settings.Distance)
		}
	}
	return nil
}
