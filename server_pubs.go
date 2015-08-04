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

type serverPublication struct {
	mu       sync.Mutex
	owner    *server
	name     string
	targetID int64

	endpoints map[localEndpointKey]*localEndpoint
	distances map[uint32]int
	settings  PublishSettings

	ready map[*localEndpoint]struct{}
	queue []chan *localEndpoint

	subscriptions map[*serverSubscription]struct{}
}

var _ endpointReference = &serverPublication{}

func (pub *serverPublication) takeEndpointLocked() *localEndpoint {
	var best *localEndpoint
	var active uint32
	for endpoint := range pub.ready {
		if best == nil || active > endpoint.active {
			best = endpoint
			active = endpoint.active
			if active == 0 {
				break
			}
		}
	}
	if best != nil {
		best.active++
		if best.active == best.settings.Concurrency {
			delete(pub.ready, best)
		}
	}
	return best
}

func (pub *serverPublication) takeEndpointRLocked() *localEndpoint {
	pub.mu.Lock()
	best := pub.takeEndpointLocked()
	pub.mu.Unlock()
	return best
}

func (pub *serverPublication) releaseEndpointRLocked(endpoint *localEndpoint) {
	if endpoint.pub == pub {
		pub.mu.Lock()
		if len(pub.queue) > 0 {
			// transfer this endpoint to someone else
			waiter := pub.queue[0]
			copy(pub.queue, pub.queue[1:])
			pub.queue[len(pub.queue)-1] = nil
			pub.queue = pub.queue[:len(pub.queue)-1]
			waiter <- endpoint
			close(waiter)
		} else {
			endpoint.active--
			endpoint.pub.ready[endpoint] = struct{}{}
		}
		pub.mu.Unlock()
	}
}

func (pub *serverPublication) queueWaiterRLocked() <-chan *localEndpoint {
	pub.mu.Lock()
	if len(pub.queue) >= int(pub.settings.QueueSize) {
		pub.mu.Unlock()
		return nil
	}
	waiter := make(chan *localEndpoint, 1)
	pub.queue = append(pub.queue, waiter)
	pub.mu.Unlock()
	return waiter
}

// Wakes up up to n waiters in the queue
// Either publication or server must be write locked
func (pub *serverPublication) wakeupWaitersLocked(n int) int {
	if n > len(pub.queue) {
		n = len(pub.queue)
	}
	if n > 0 {
		for i := 0; i < n; i++ {
			endpoint := pub.takeEndpointLocked()
			if endpoint == nil {
				n = i
				break
			}
			pub.queue[i] <- endpoint
			close(pub.queue[i])
		}
		live := copy(pub.queue, pub.queue[n:])
		for i := live; i < len(pub.queue); i++ {
			pub.queue[i] = nil
		}
		pub.queue = pub.queue[:live]
	}
	return n
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
	for len(pub.endpoints) > 0 {
		endpoint := pub.takeEndpointRLocked()
		if endpoint == nil {
			waiter := pub.queueWaiterRLocked()
			if waiter == nil {
				// Queue for this publication is already full
				return handleRequestStatusOverCapacity
			}
			pub.owner.mu.RUnlock()
			endpoint = <-waiter
			pub.owner.mu.RLock()
			if endpoint == nil {
				// Queue overflowed due to unpublish while we waited
				return handleRequestStatusOverCapacity
			}
			if endpoint.pub == nil {
				// This endpoint got unpublished while we waited
				continue
			}
		}
		status := pub.handleRequestWithRUnlock(endpoint.key, callback)
		pub.releaseEndpointRLocked(endpoint)
		// FIXME: process handleRequestStatusImpossible?
		return status
	}
	return handleRequestStatusNoRoute
}

func (pub *serverPublication) handleRequestWithRUnlock(key localEndpointKey, callback handleRequestCallback) handleRequestStatus {
	pub.owner.mu.RUnlock()
	defer pub.owner.mu.RLock()
	remote, err := rpcNewStream(key.client.conn, key.targetID)
	if err != nil {
		// this client has already disconnected
		return handleRequestStatusImpossible
	}
	defer remote.Close()
	return callback(remote)
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

	pub.wakeupWaitersLocked(int(settings.Concurrency))

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
