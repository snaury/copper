package copper

import (
	"sync"
)

// rawConnPings handles expected ping replies
type rawConnPings struct {
	conn *rawConn

	mu sync.Mutex

	err   error                   // becomes non-nil when the connection fails
	pings map[int64][]func(error) // map from ping value to a list of callbacks
}

func (p *rawConnPings) init(conn *rawConn) {
	p.conn = conn
	p.pings = make(map[int64][]func(error))
}

// Immediately fails all current pings with err.
// Pings that are scheduled might or might not be sent on the wire, however
// their potential replies would be ignored either way. This function is
// usually called in the readloop and implies that no further frames may be
// received.
func (p *rawConnPings) fail(err error) {
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	p.conn.outgoing.clearPingQueue()
	pings := p.pings
	p.pings = nil
	p.mu.Unlock()
	for _, callbacks := range pings {
		for _, callback := range callbacks {
			callback(err)
		}
	}
}

// Schedules an outgoing ping, registering callback to be called when reply
// is received. Care must be taken to not block in the callback, since that
// might prevent further frames from being read.
func (p *rawConnPings) addPing(value int64, callback func(error)) error {
	p.mu.Lock()
	err := p.err
	if err == nil {
		p.pings[value] = append(p.pings[value], callback)
		p.conn.outgoing.addPingQueue(value)
	}
	p.mu.Unlock()
	return err
}

// Handles an incoming ping requests. Schedules an outgoing reply.
func (p *rawConnPings) handlePing(value int64) {
	p.conn.outgoing.addPingAck(value)
}

// Handles an incoming ping reply. Run a registered callback, if found.
func (p *rawConnPings) handleAck(value int64) {
	var callback func(error)
	p.mu.Lock()
	callbacks := p.pings[value]
	if len(callbacks) > 0 {
		callback = callbacks[0]
		if len(callbacks) > 1 {
			copy(callbacks, callbacks[1:])
			callbacks[len(callbacks)-1] = nil
			p.pings[value] = callbacks[:len(callbacks)-1]
		} else {
			delete(p.pings, value)
		}
	}
	p.mu.Unlock()
	if callback != nil {
		callback(nil)
	}
}
