package copper

import (
	"time"
)

// deadlineChannel sets up goroutine-safe deadline channels
// The struct itself must be protected with an outside lock
type deadlineChannel struct {
	timer    *time.Timer
	expired  chan struct{}
	deadline time.Time
}

var zeroTime time.Time

func (c *deadlineChannel) setDeadline(t time.Time) {
	if c.deadline != t {
		c.deadline = t
		if c.timer != nil && c.expired != nil && !c.timer.Stop() {
			// Previous timer was not active, which may only happen if it has
			// expired. We must wait on the channel to prevent races.
			<-c.expired
			c.expired = nil
		}
		if t == zeroTime {
			// Setting deadline to zero is equivalent to no deadline
			c.expired = nil
			return
		}
		if c.expired == nil {
			// Create a new channel that will be closed at the deadline
			c.expired = make(chan struct{})
		}
		if c.timer != nil {
			// Reuse previous timer by resetting it to the new deadline
			c.timer.Reset(t.Sub(time.Now()))
		} else {
			// Create a new timer that will close the channel at expiration
			// This timer will be reused, so it is very important to ensure
			// that the callback will only ever be called once per deadline
			c.timer = time.AfterFunc(t.Sub(time.Now()), func() {
				close(c.expired)
			})
		}
	}
}
