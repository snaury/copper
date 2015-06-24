package copper

import (
	"bufio"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	defaultConnWindowSize    = 65536
	defaultStreamWindowSize  = 65536
	defaultInactivityTimeout = 60 * time.Second
)

// Conn is a multiplexed connection implementing the copper protocol
type Conn interface {
	Wait() error
	Close() error
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	OpenStream(target int64) (s Stream, err error)
}

type rawConn struct {
	lock                    sync.Mutex
	conn                    net.Conn
	closed                  bool
	closedcond              sync.Cond
	failure                 error
	signal                  chan struct{}
	handler                 StreamHandler
	streams                 map[int]*stream
	deadstreams             map[int]struct{}
	freestreams             map[int]struct{}
	nextnewstream           int
	pingAcks                []uint64
	pingQueue               []uint64
	pingResults             map[uint64][]chan error
	outgoingAcks            map[int]int
	outgoingData            map[int]struct{}
	writeleft               int
	localConnWindowSize     int
	remoteConnWindowSize    int
	localStreamWindowSize   int
	remoteStreamWindowSize  int
	localInactivityTimeout  time.Duration
	remoteInactivityTimeout time.Duration
}

// NewConn wraps the underlying network connection with the copper protocol
func NewConn(conn net.Conn, handler StreamHandler) Conn {
	c := &rawConn{
		conn:                    conn,
		closed:                  false,
		signal:                  make(chan struct{}, 1),
		handler:                 handler,
		streams:                 make(map[int]*stream),
		deadstreams:             make(map[int]struct{}),
		freestreams:             make(map[int]struct{}),
		nextnewstream:           1,
		pingResults:             make(map[uint64][]chan error),
		outgoingAcks:            make(map[int]int),
		outgoingData:            make(map[int]struct{}),
		writeleft:               defaultConnWindowSize,
		localConnWindowSize:     defaultConnWindowSize,
		remoteConnWindowSize:    defaultConnWindowSize,
		localStreamWindowSize:   defaultStreamWindowSize,
		remoteStreamWindowSize:  defaultStreamWindowSize,
		localInactivityTimeout:  defaultInactivityTimeout,
		remoteInactivityTimeout: defaultInactivityTimeout,
	}
	c.closedcond.L = &c.lock
	go c.readloop()
	go c.writeloop()
	return c
}

func (c *rawConn) wakeupLocked() {
	if !c.closed {
		select {
		case c.signal <- struct{}{}:
		default:
		}
	}
}

func (c *rawConn) closeWithErrorLocked(err error) error {
	if !c.closed {
		c.closed = true
		c.failure = err
		close(c.signal)
		c.closedcond.Broadcast()
		return nil
	}
	return c.failure
}

func (c *rawConn) closeWithError(err error) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.closeWithErrorLocked(err)
}

func (c *rawConn) Wait() error {
	c.lock.Lock()
	defer c.lock.Unlock()
	for !c.closed {
		c.closedcond.Wait()
	}
	return c.failure
}

func (c *rawConn) Close() error {
	return c.closeWithError(ECONNCLOSED)
}

func (c *rawConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *rawConn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *rawConn) OpenStream(target int64) (Stream, error) {
	// TODO
	return nil, nil
}

func (c *rawConn) addPingAck(value uint64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.pingAcks = append(c.pingAcks, value)
	c.wakeupLocked()
}

func (c *rawConn) takePingResult(value uint64) (result chan error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	results, ok := c.pingResults[value]
	if ok {
		result = results[0]
		if len(results) > 1 {
			resultscopy := make([]chan error, len(results)-1)
			copy(resultscopy, results[1:])
			c.pingResults[value] = resultscopy
		} else {
			delete(c.pingResults, value)
		}
	}
	return
}

func (c *rawConn) addPingRequest(value uint64, result chan error) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		return c.failure
	}
	c.pingResults[value] = append(c.pingResults[value], result)
	c.pingQueue = append(c.pingQueue, value)
	return nil
}

func (c *rawConn) failAllPings() {
	c.lock.Lock()
	defer c.lock.Unlock()
	pingResults := c.pingResults
	c.pingQueue = nil
	c.pingResults = nil
	for _, results := range pingResults {
		for _, result := range results {
			select {
			case result <- c.failure:
			default:
			}
			close(result)
		}
	}
}

func (c *rawConn) addOutgoingAckLocked(streamID int, increment int) {
	if !c.closed {
		old := c.outgoingAcks[streamID]
		c.outgoingAcks[streamID] = old + increment
		if old == 0 {
			c.wakeupLocked()
		}
	}
}

func (c *rawConn) addOutgoingDataLocked(streamID int) {
	if !c.closed {
		_, ok := c.outgoingData[streamID]
		if !ok {
			c.outgoingData[streamID] = struct{}{}
			c.wakeupLocked()
		}
	}
}

func (c *rawConn) incrementWindowLocked(n int) {
	c.lock.Lock()
	defer c.lock.Unlock()
	old := c.writeleft
	c.writeleft = old + n
	if old == 0 {
		c.wakeupLocked()
	}
}

func (c *rawConn) handleStream(target int64, s *stream) {
	if c.handler != nil {
		defer s.Close()
		c.handler.HandleStream(target, s)
	} else {
		s.CloseWithError(ENOTARGET)
	}
}

func (c *rawConn) cleanupStreamLocked(s *stream) {
	if s.isFullyClosed() {
		// connection is fully closed and may be forgotten
		delete(c.streams, s.streamID)
		if s.streamID&1 == 1 {
			// this is local stream, it is no longer in use, but when
			// confirmed might be up for reuse
			c.deadstreams[s.streamID] = struct{}{}
		}
	}
}

func (c *rawConn) processOpenFrame(frame openFrame) error {
	if frame.streamID <= 0 || frame.streamID&1 != 0 {
		return EINVALIDSTREAM
	}
	if len(frame.data) > c.localStreamWindowSize {
		return EWINDOWOVERFLOW
	}
	s := newIncomingStream(c, frame)
	s.writeleft = c.remoteStreamWindowSize
	c.lock.Lock()
	defer c.lock.Unlock()
	if len(frame.data) > 0 {
		c.addOutgoingAckLocked(0, len(frame.data))
	}
	old := c.streams[frame.streamID]
	if old != nil {
		return &errorWithReason{
			error:  fmt.Errorf("stream 0x%08x cannot be opened until fully closed"),
			reason: EINVALIDSTREAM,
		}
	}
	c.streams[frame.streamID] = s
	go c.handleStream(frame.targetID, s)
	return nil
}

func (c *rawConn) processDataFrame(frame dataFrame) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	if len(frame.data) > 0 {
		c.addOutgoingAckLocked(0, len(frame.data))
	}
	s := c.streams[frame.streamID]
	if s == nil {
		return &errorWithReason{
			error:  fmt.Errorf("stream 0x%08x is unknown"),
			reason: EINVALIDSTREAM,
		}
	}
	err := s.processDataFrameLocked(frame)
	if err != nil {
		return err
	}
	c.cleanupStreamLocked(s)
	return nil
}

func (c *rawConn) processResetFrame(frame resetFrame) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	s := c.streams[frame.streamID]
	if s == nil {
		// it's ok to receive reset frames for unknown streams
		return nil
	}
	err := s.processResetFrameLocked(frame)
	if err != nil {
		return err
	}
	c.cleanupStreamLocked(s)
	return nil
}

func (c *rawConn) processWindowFrame(frame windowFrame) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	if frame.streamID == 0 {
		c.incrementWindowLocked(frame.increment)
		return nil
	}
	s := c.streams[frame.streamID]
	if s != nil {
		s.incrementWindowLocked(frame.increment)
	}
	return nil
}

func (c *rawConn) readloop() {
	r := bufio.NewReader(c.conn)
readloop:
	for {
		c.conn.SetReadDeadline(time.Now().Add(c.localInactivityTimeout))
		rawFrame, err := readFrame(r)
		if err != nil {
			c.closeWithError(err)
			break readloop
		}
		switch frame := rawFrame.(type) {
		case pingFrame:
			if (frame.flags & flagAck) != 0 {
				result := c.takePingResult(frame.value)
				if result != nil {
					select {
					case result <- nil:
					default:
					}
					close(result)
				}
			} else {
				c.addPingAck(frame.value)
			}
		case openFrame:
			err = c.processOpenFrame(frame)
			if err != nil {
				c.closeWithError(err)
				break readloop
			}
		case dataFrame:
			err = c.processDataFrame(frame)
			if err != nil {
				c.closeWithError(err)
				break readloop
			}
		case resetFrame:
			err = c.processResetFrame(frame)
			if err != nil {
				c.closeWithError(err)
				break readloop
			}
		case windowFrame:
			err = c.processWindowFrame(frame)
			if err != nil {
				c.closeWithError(err)
				break readloop
			}
		default:
			panic("unhandled frame type")
		}
	}
	c.failAllPings()
}

func (c *rawConn) prepareWriteBatch(datarequired bool) (frames []frame, err error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		return nil, c.failure
	}
	if len(c.pingQueue) > 0 {
		for _, value := range c.pingQueue {
			frames = append(frames, pingFrame{
				value: value,
			})
		}
		c.pingQueue = c.pingQueue[:0]
		return
	}
	if len(c.outgoingAcks) > 0 {
		for streamID, increment := range c.outgoingAcks {
			frames = append(frames, windowFrame{
				streamID:  streamID,
				increment: increment,
			})
			delete(c.outgoingAcks, streamID)
		}
		return
	}
	if len(c.outgoingData) > 0 && c.writeleft > 0 {
		var sent int
		for streamID := range c.outgoingData {
			s := c.streams[streamID]
			if s == nil {
				// the connection is already gone
				delete(c.outgoingData, streamID)
				continue
			}
			frames, sent = s.outgoingFramesLocked(frames, c.writeleft)
			if !s.active() {
				delete(c.outgoingData, streamID)
			}
			c.writeleft -= sent
			if c.writeleft == 0 {
				break
			}
		}
		return
	}
	if datarequired {
		frames = append(frames, dataFrame{
			streamID: 0,
			data:     nil,
		})
	}
	return
}

func (c *rawConn) writeloop() {
	datarequired := false
	w := bufio.NewWriter(c.conn)
	t := time.NewTimer(2 * c.remoteInactivityTimeout / 3)
	for {
		select {
		case <-c.signal:
			if !t.Stop() {
				// If t has expired we need to drain the channel to prevent
				// spurios activation on the next iteration.
				<-t.C
			}
		case <-t.C:
			datarequired = true
		}
		for {
			frames, err := c.prepareWriteBatch(datarequired)
			if err != nil {
				// Attempt to notify the other side that we have an error
				// It's ok if any of this fails, the read side will stop
				// when we close the connection.
				c.conn.SetWriteDeadline(time.Now().Add(c.localInactivityTimeout))
				errorToFatalFrame(err).writeFrameTo(w)
				w.Flush()
				c.conn.Close()
				return
			}
			// if there are no frames to send we may go to sleep
			datarequired = false
			if len(frames) == 0 {
				break
			}
			// send all frames that have been accumulated
			for _, frame := range frames {
				c.conn.SetWriteDeadline(time.Now().Add(c.localInactivityTimeout))
				err = frame.writeFrameTo(w)
				if err != nil {
					c.closeWithError(err)
					c.conn.Close()
					return
				}
			}
			// clear the signal flag before the next iteration
			select {
			case <-c.signal:
			default:
			}
		}
		// we must flush all accumulated data before going to sleep
		if w.Buffered() > 0 {
			c.conn.SetWriteDeadline(time.Now().Add(c.localInactivityTimeout))
			err := w.Flush()
			if err != nil {
				c.closeWithError(err)
				c.conn.Close()
				return
			}
		}
		t.Reset(2 * c.remoteInactivityTimeout / 3)
	}
}
