package copper

import (
	"sync"
)

// rawConnOutgoing handles all outgoing data.
type rawConnOutgoing struct {
	conn *rawConn

	// This mutex protects all fields of this structure, but a very important
	// invariant is that this mutex must be a leaf amongst other connection
	// related mutexes. It must not be locked when calling any outside code.
	mu sync.Mutex

	err        error     // becomes non-nil when the connection fails
	writeready bool      // true if there exists something to write
	writecond  sync.Cond // signals when writeready becomes true

	blocked   int       // blocks writing when >0, used during testing
	unblocked sync.Cond // signals when blocked becomes 0

	pingAcks  []PingData // a list of outgoing ping replies
	pingQueue []PingData // a list of outgoing ping requests

	settingsAcks int // number of SETTINGS/ACK frames that need to be sent

	ctrl rawStreamQueue // a queue of streams that need to write control frames
	data rawStreamQueue // a queue of streams that need to write data
}

func (o *rawConnOutgoing) init(conn *rawConn) {
	o.conn = conn
	o.writecond.L = &o.mu
	o.unblocked.L = &o.mu
}

// Schedules a send of err to the remote side. If the error is a normal close
// of the connection it is changed to ECONNSHUTDOWN. The connection will be
// closed after the error is sent, freeing resources.
func (o *rawConnOutgoing) fail(err error) {
	o.mu.Lock()
	if o.err == nil {
		if err == ECONNCLOSED {
			o.err = ECONNSHUTDOWN
		} else {
			o.err = err
		}
		o.wakeupLocked()
	}
	o.mu.Unlock()
}

// Schedules a ping reply on the connection.
func (o *rawConnOutgoing) addPingAck(value PingData) {
	o.mu.Lock()
	o.pingAcks = append(o.pingAcks, value)
	o.wakeupLocked()
	o.mu.Unlock()
}

// Schedules a ping request on the connection.
func (o *rawConnOutgoing) addPingQueue(value PingData) {
	o.mu.Lock()
	if o.err == nil {
		o.pingQueue = append(o.pingQueue, value)
		o.wakeupLocked()
	}
	o.mu.Unlock()
}

// Clears the ping request queue, used by other components to cancel pings.
func (o *rawConnOutgoing) clearPingQueue() {
	o.mu.Lock()
	o.pingQueue = nil
	o.mu.Unlock()
}

// Schedules a SETTINGS frame with the ACK flag on the connection.
func (o *rawConnOutgoing) addSettingsAck() {
	o.mu.Lock()
	o.settingsAcks++
	o.wakeupLocked()
	o.mu.Unlock()
}

// Adds stream to the control frames queue
func (o *rawConnOutgoing) addCtrl(stream *rawStream) {
	o.mu.Lock()
	if !stream.inctrl {
		o.ctrl.push(stream)
		stream.inctrl = true
		o.wakeupLocked()
	}
	o.mu.Unlock()
}

// Adds stream to the data queue
func (o *rawConnOutgoing) addData(stream *rawStream) {
	o.mu.Lock()
	if !stream.indata {
		o.data.push(stream)
		stream.indata = true
		o.wakeupLocked()
	}
	o.mu.Unlock()
}

// Wakes up writeloop
func (o *rawConnOutgoing) wakeup() {
	o.mu.Lock()
	o.wakeupLocked()
	o.mu.Unlock()
}

// Wakes up writeloop, mu must be locked
func (o *rawConnOutgoing) wakeupLocked() {
	o.writeready = true
	o.writecond.Broadcast()
}

// Writes outgoing frames until there is nothing more to send. Returns false on
// errors, or after sending an error to the remote, indicating that the
// connection should be closed.
func (o *rawConnOutgoing) writeFrames() (result bool) {
	o.mu.Lock()
	for !o.writeready {
		o.writecond.Wait()
	}

writeloop:
	for {
		for o.blocked > 0 {
			// Allow tests to setup things properly
			o.unblocked.Wait()
		}
		if o.err != nil {
			err := o.err
			o.mu.Unlock()
			// Attempt to notify the other side that we have an error
			// It's ok if any of this fails, the read side will stop
			// when we close the connection.
			o.conn.writer.WriteReset(0, 0, err)
			return false
		}
		pingAcks := o.pingAcks
		if len(pingAcks) > 0 {
			o.pingAcks = nil
			o.mu.Unlock()
			for _, data := range pingAcks {
				err := o.conn.writer.WritePing(FlagPingAck, data)
				if err != nil {
					o.conn.closeWithError(err)
					return false
				}
			}
			o.mu.Lock()
			continue writeloop
		}
		pingQueue := o.pingQueue
		if len(pingQueue) > 0 {
			o.pingQueue = nil
			o.mu.Unlock()
			for _, data := range pingQueue {
				err := o.conn.writer.WritePing(0, data)
				if err != nil {
					o.conn.closeWithError(err)
					return false
				}
			}
			o.mu.Lock()
			continue writeloop
		}
		if o.settingsAcks > 0 {
			o.settingsAcks--
			o.mu.Unlock()
			err := o.conn.writer.WriteSettingsAck()
			if err != nil {
				o.conn.closeWithError(err)
				return false
			}
			o.mu.Lock()
			continue writeloop
		}
		if o.ctrl.size > 0 {
			stream := o.ctrl.take()
			stream.inctrl = false
			o.mu.Unlock()
			err := stream.writeCtrl()
			if err != nil {
				o.conn.closeWithError(err)
				return false
			}
			o.mu.Lock()
			continue writeloop
		}
		if o.data.size > 0 {
			stream := o.data.take()
			stream.indata = false
			o.mu.Unlock()
			err := stream.writeData()
			if err != nil {
				o.conn.closeWithError(err)
				return false
			}
			o.mu.Lock()
			continue writeloop
		}
		break
	}
	// If we reach here there's nothing to send
	o.writeready = false
	o.mu.Unlock()
	return true
}
