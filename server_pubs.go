package copper

import (
	"fmt"
	"sync/atomic"
)

type localEndpointKey struct {
	client   *serverClient
	targetID int64
}

type localEndpoint struct {
	owner    *server
	pub      *serverPublication
	key      localEndpointKey
	settings PublishSettings
	stopped  chan struct{}
}

type localEndpointRequest struct {
	callback handleRequestCallback
	reply    chan<- handleRequestStatus
}

type serverPublication struct {
	owner    *server
	name     string
	targetID int64

	endpoints map[localEndpointKey]*localEndpoint
	distances map[uint32]int
	settings  PublishSettings

	active   uint32
	stopped  chan struct{}
	requests chan localEndpointRequest

	subscriptions map[*serverSubscription]struct{}
}

var _ endpointReference = &serverPublication{}

func (pub *serverPublication) takeActiveRLocked() bool {
	for atomic.AddUint32(&pub.active, 1) > pub.settings.Concurrency+pub.settings.QueueSize {
		if atomic.AddUint32(&pub.active, 0xffffffff) >= pub.settings.Concurrency+pub.settings.QueueSize {
			return false
		}
	}
	return true
}

func (pub *serverPublication) releaseActiveRLocked() {
	atomic.AddUint32(&pub.active, 0xffffffff)
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

func (pub *serverPublication) handleRequests(endpoint *localEndpoint) {
	for {
		select {
		case req := <-pub.requests:
			remote, err := rpcNewStream(endpoint.key.client.conn, endpoint.key.targetID)
			if err != nil {
				// this client has already disconnected
				req.reply <- handleRequestStatusImpossible
			}
			req.reply <- req.callback(remote)
			remote.Close()
		case <-endpoint.stopped:
			return
		}
	}
}

func (pub *serverPublication) handleRequestRLocked(callback handleRequestCallback) handleRequestStatus {
	if pub.takeActiveRLocked() {
		defer pub.releaseActiveRLocked()
		reply := make(chan handleRequestStatus, 1)
		req := localEndpointRequest{
			callback: callback,
			reply:    reply,
		}
		pub.owner.mu.RUnlock()
		select {
		case pub.requests <- req:
		case <-pub.stopped:
			pub.owner.mu.RLock()
			return handleRequestStatusNoRoute
		}
		status := <-reply
		pub.owner.mu.RLock()
		return status
	}
	return handleRequestStatusOverCapacity
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

			stopped:  make(chan struct{}),
			requests: make(chan localEndpointRequest),

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
		stopped:  make(chan struct{}),
		settings: settings,
	}

	pub.endpoints[endpoint.key] = endpoint
	for i := uint32(0); i < endpoint.settings.Concurrency; i++ {
		go pub.handleRequests(endpoint)
	}

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

	close(endpoint.stopped)
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
		close(pub.stopped)
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
		// FIXME: how do we cancel requests over queue size?
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
