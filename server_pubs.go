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

func (endpoint *localEndpoint) getEndpointsLocked() []Endpoint {
	return nil
}

func (endpoint *localEndpoint) handleRequestLocked(callback handleRequestCallback) handleRequestStatus {
	if endpoint.pub != nil && endpoint.active < endpoint.settings.Concurrency {
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
		remote, err := rpcNewStream(endpoint.key.client.conn, endpoint.key.targetID)
		if err != nil {
			// this client has already disconnected
			return handleRequestStatusImpossible
		}
		defer remote.Close()
		return callback(remote)
	}
	return handleRequestStatusOverCapacity
}

type serverPublication struct {
	owner    *server
	name     string
	targetID int64

	endpoints map[localEndpointKey]*localEndpoint
	distances map[uint32]int
	settings  PublishSettings

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

func (pub *serverPublication) handleRequestLocked(callback handleRequestCallback) handleRequestStatus {
	var waiter *sync.Cond
reqloop:
	for len(pub.endpoints) > 0 {
		for endpoint := range pub.ready {
			switch status := endpoint.handleRequestLocked(callback); status {
			case handleRequestStatusDone:
				return status
			case handleRequestStatusImpossible:
				// the endpoint is gone, we might just not know it yet
				endpoint.unpublishLocked()
				continue reqloop
			case handleRequestStatusOverCapacity:
				// there are not enough free slots, try the next one
			default:
				// local endpoints shouldn't return anything else
				return status
			}
		}
		if len(pub.queue) >= int(pub.settings.QueueSize) {
			// Queue for this publication is already full
			return handleRequestStatusOverCapacity
		}
		if waiter == nil {
			// TODO: support for timeout and cancellation
			waiter = sync.NewCond(&pub.owner.lock)
		}
		pub.waitInQueueLocked(waiter)
	}
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
			queue: make(map[*sync.Cond]struct{}),

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
		for waiter := range pub.queue {
			waiter.Signal()
		}
		for watcher := range pub.owner.pubWatchers {
			watcher.addRemovedLocked(pub)
		}
		for sub := range pub.subscriptions {
			sub.removePublicationLocked(pub)
		}
	} else {
		if len(pub.queue) > int(pub.settings.QueueSize) {
			pub.wakeupWaitersLocked(int(pub.settings.QueueSize) - len(pub.queue))
		}
		for watcher := range pub.owner.pubWatchers {
			watcher.addChangedLocked(pub)
		}
	}
	return nil
}
