package copper

import (
	"sync"
)

type rawConnPings struct {
	mu    sync.Mutex
	owner *rawConn
	err   error
	pings map[int64][]func(error)
}

func (p *rawConnPings) init(owner *rawConn) {
	p.owner = owner
	p.pings = make(map[int64][]func(error))
}

func (p *rawConnPings) fail(err error) {
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	pings := p.pings
	p.pings = nil
	p.mu.Unlock()
	for _, callbacks := range pings {
		for _, callback := range callbacks {
			callback(err)
		}
	}
}

func (p *rawConnPings) handlePing(value int64) {
	p.owner.outgoing.addPingAck(value)
}

func (p *rawConnPings) addPing(value int64, callback func(error)) error {
	p.mu.Lock()
	err := p.err
	if err == nil {
		p.pings[value] = append(p.pings[value], callback)
		p.owner.outgoing.addPingQueue(value)
	}
	p.mu.Unlock()
	return err
}

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
