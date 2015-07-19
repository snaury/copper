package copper

import (
	"sync"
)

type rawConnOutgoing struct {
	mu         sync.Mutex
	owner      *rawConn
	failure    error
	writeleft  int
	writecond  sync.Cond
	writeready bool

	blocked   int
	unblocked sync.Cond

	pingAcks  []int64
	pingQueue []int64

	settingsAcks int

	remoteIncrement int

	ctrl rawStreamQueue
	data rawStreamQueue

	wakeupMu     sync.Mutex
	wakeupCond   sync.Cond
	wakeupNeeded bool
}

func (o *rawConnOutgoing) init(owner *rawConn) {
	o.owner = owner
	o.writeleft = defaultConnWindowSize
	o.writecond.L = &o.mu
	o.unblocked.L = &o.mu
}

func (o *rawConnOutgoing) fail(err error) {
	o.mu.Lock()
	if o.failure == nil {
		if err == ECONNCLOSED {
			o.failure = ECONNSHUTDOWN
		} else {
			o.failure = err
		}
		// don't send pings that haven't been sent already
		o.pingQueue = nil
		o.wakeupLocked()
	}
	o.mu.Unlock()
}

func (o *rawConnOutgoing) takeSpace(n int) int {
	o.mu.Lock()
	if n > o.writeleft {
		n = o.writeleft
	}
	if n < 0 {
		n = 0
	}
	o.writeleft -= n
	o.mu.Unlock()
	return n
}

func (o *rawConnOutgoing) changeWindow(increment int) {
	o.mu.Lock()
	o.writeleft += increment
	if o.writeleft > 0 && o.data.size > 0 {
		o.wakeupLocked()
	}
	o.mu.Unlock()
}

func (o *rawConnOutgoing) addPingAck(value int64) {
	o.mu.Lock()
	o.pingAcks = append(o.pingAcks, value)
	o.wakeupLocked()
	o.mu.Unlock()
}

func (o *rawConnOutgoing) addPingQueue(value int64) {
	o.mu.Lock()
	if o.failure == nil {
		o.pingQueue = append(o.pingQueue, value)
		o.wakeupLocked()
	}
	o.mu.Unlock()
}

func (o *rawConnOutgoing) addSettingsAck() {
	o.mu.Lock()
	o.settingsAcks++
	o.wakeupLocked()
	o.mu.Unlock()
}

func (o *rawConnOutgoing) incrementRemote(increment int) {
	o.mu.Lock()
	o.remoteIncrement += increment
	o.wakeupLocked()
	o.mu.Unlock()
}

func (o *rawConnOutgoing) addCtrl(stream *rawStream) {
	o.mu.Lock()
	if !stream.inctrl {
		o.ctrl.push(stream)
		stream.inctrl = true
		o.wakeupLocked()
	}
	o.mu.Unlock()
}

func (o *rawConnOutgoing) addData(stream *rawStream) {
	o.mu.Lock()
	if !stream.indata {
		o.data.push(stream)
		stream.indata = true
		o.wakeupLocked()
	}
	o.mu.Unlock()
}

func (o *rawConnOutgoing) wakeupLocked() {
	o.writeready = true
	o.writecond.Broadcast()
}

func (o *rawConnOutgoing) wait() {
	o.mu.Lock()
	for !o.writeready {
		o.writecond.Wait()
	}
	o.writeready = false
	o.mu.Unlock()
}
