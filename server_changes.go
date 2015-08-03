package copper

import (
	"io"
	"sync"
)

const maxChangedServicesBatch = 256

type serverServiceChangesStream struct {
	owner   *server
	client  *serverClient
	closed  bool
	wakeup  sync.Cond
	sent    map[int64]struct{}
	removed map[int64]struct{}
	changed map[int64]*serverPublication
}

var _ ServiceChangesStream = &serverServiceChangesStream{}

func newServerServiceChangesStream(s *server, c *serverClient) *serverServiceChangesStream {
	cs := &serverServiceChangesStream{
		owner:   s,
		client:  c,
		sent:    make(map[int64]struct{}),
		removed: make(map[int64]struct{}),
		changed: make(map[int64]*serverPublication),
	}
	cs.wakeup.L = &s.mu
	// send all services that are currently active
	for _, pub := range s.pubByTarget {
		if len(pub.endpoints) > 0 {
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

func (cs *serverServiceChangesStream) addRemovedLocked(pub *serverPublication) {
	if !cs.closed {
		delete(cs.changed, pub.targetID)
		if _, ok := cs.sent[pub.targetID]; ok {
			cs.removed[pub.targetID] = struct{}{}
			cs.wakeup.Broadcast()
		}
	}
}

func (cs *serverServiceChangesStream) addChangedLocked(pub *serverPublication) {
	if !cs.closed {
		delete(cs.removed, pub.targetID)
		cs.changed[pub.targetID] = pub
		cs.wakeup.Broadcast()
	}
}

func (cs *serverServiceChangesStream) Read() (ServiceChanges, error) {
	cs.owner.mu.Lock()
	defer cs.owner.mu.Unlock()
	for {
		if cs.closed {
			return ServiceChanges{}, io.EOF
		}
		var changes ServiceChanges
		for targetID := range cs.removed {
			delete(cs.sent, targetID)
			delete(cs.removed, targetID)
			changes.Removed = append(changes.Removed, targetID)
		}
		for targetID, pub := range cs.changed {
			cs.sent[targetID] = struct{}{}
			delete(cs.changed, targetID)
			changes.Changed = append(changes.Changed, ServiceChange{
				TargetID: targetID,
				Name:     pub.name,
				Settings: pub.settings,
			})
			if len(changes.Changed) >= maxChangedServicesBatch {
				break
			}
		}
		if len(changes.Removed) > 0 || len(changes.Changed) > 0 {
			return changes, nil
		}
		cs.wakeup.Wait()
	}
}

func (cs *serverServiceChangesStream) stopLocked() error {
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

func (cs *serverServiceChangesStream) Stop() error {
	cs.owner.mu.Lock()
	defer cs.owner.mu.Unlock()
	return cs.stopLocked()
}
