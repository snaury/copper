package raw

import (
	"sync"
	"time"
)

type expiration struct {
	t       *time.Timer
	cond    *sync.Cond
	expired bool
}

func newExpiration(cond *sync.Cond, d time.Duration) *expiration {
	e := &expiration{
		cond: cond,
	}
	e.t = time.AfterFunc(d, e.callback)
	return e
}

func (e *expiration) callback() {
	e.cond.L.Lock()
	defer e.cond.L.Unlock()
	if !e.expired {
		e.expired = true
		e.cond.Broadcast()
	}
}

func (e *expiration) stop() bool {
	if e.t != nil {
		return e.t.Stop()
	}
	return false
}

type condWithDeadline struct {
	sync.Cond
	deadline time.Time
	exp      *expiration
}

func (c *condWithDeadline) init(lock sync.Locker) {
	c.Cond.L = lock
}

func (c *condWithDeadline) setDeadline(t time.Time) {
	if c.deadline != t {
		if c.exp != nil {
			c.exp.stop()
			c.exp = nil
		}
		c.deadline = t
		if !t.IsZero() {
			// Since deadline has changed Wait needs to wake up and recheck
			c.Broadcast()
		}
	}
}

func (c *condWithDeadline) Wait() error {
	if c.exp == nil && !c.deadline.IsZero() {
		d := c.deadline.Sub(time.Now())
		if d <= 0 {
			return errTimeout
		}
		c.exp = newExpiration(&c.Cond, d)
	}
	c.Cond.Wait()
	if c.exp != nil && c.exp.expired {
		return errTimeout
	}
	return nil
}
