package copperd

import (
	"io"
	"sync"
)

type serverServiceChangeStream struct {
	owner   *server
	client  *serverClient
	closed  bool
	wakeup  sync.Cond
	removed map[int64]*serverPublication
	changed map[int64]*serverPublication
}

var _ ServiceChangeStream = &serverServiceChangeStream{}

func newServerServiceChangeStream(s *server, c *serverClient) *serverServiceChangeStream {
	cs := &serverServiceChangeStream{
		owner:   s,
		client:  c,
		removed: make(map[int64]*serverPublication),
		changed: make(map[int64]*serverPublication),
	}
	cs.wakeup.L = &s.lock
	// send all services that are currently active
	for _, pub := range s.pubByTarget {
		if len(pub.localEndpoints) > 0 {
			cs.changed[pub.targetID] = pub
		}
	}
	// register ourselves
	s.pubWatchers[cs] = struct{}{}
	if c != nil {
		c.pubWatchers[cs] = struct{}{}
	}
	return cs
}

func (cs *serverServiceChangeStream) addRemoved(pub *serverPublication) {
	if !cs.closed {
		delete(cs.changed, pub.targetID)
		cs.removed[pub.targetID] = pub
	}
}

func (cs *serverServiceChangeStream) addChanged(pub *serverPublication) {
	if !cs.closed {
		delete(cs.removed, pub.targetID)
		cs.changed[pub.targetID] = pub
	}
}

func (cs *serverServiceChangeStream) Read() (ServiceChange, error) {
	cs.owner.lock.Lock()
	defer cs.owner.lock.Unlock()
	for {
		if cs.closed {
			return ServiceChange{}, io.EOF
		}
		for targetID, pub := range cs.removed {
			delete(cs.removed, targetID)
			return ServiceChange{
				TargetID: -1,
				Name:     pub.name,
			}, nil
		}
		for targetID, pub := range cs.changed {
			delete(cs.changed, targetID)
			change := ServiceChange{
				TargetID:    targetID,
				Name:        pub.name,
				Concurrency: pub.localConcurrency,
			}
			for distance := range pub.localDistances {
				if change.Distance < distance {
					change.Distance = distance
				}
			}
			return change, nil
		}
		cs.wakeup.Wait()
	}
}

func (cs *serverServiceChangeStream) stopLocked() error {
	if !cs.closed {
		cs.closed = true
		delete(cs.owner.pubWatchers, cs)
		if cs.client != nil {
			delete(cs.client.pubWatchers, cs)
		}
		cs.wakeup.Broadcast()
	}
	return nil
}

func (cs *serverServiceChangeStream) Stop() error {
	cs.owner.lock.Lock()
	defer cs.owner.lock.Unlock()
	return cs.stopLocked()
}
