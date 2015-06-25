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

var _ Conn = &rawConn{}

type rawConn struct {
	lock                    sync.Mutex
	waitready               sync.Cond
	conn                    net.Conn
	isserver                bool
	closed                  bool
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
	outgoingCtrl            map[int]struct{}
	outgoingData            map[int]struct{}
	outgoingFailure         error
	writeleft               int
	localConnWindowSize     int
	remoteConnWindowSize    int
	localStreamWindowSize   int
	remoteStreamWindowSize  int
	localInactivityTimeout  time.Duration
	remoteInactivityTimeout time.Duration
}

// NewConn wraps the underlying network connection with the copper protocol
func NewConn(conn net.Conn, handler StreamHandler, isserver bool) Conn {
	c := &rawConn{
		conn:                    conn,
		isserver:                isserver,
		closed:                  false,
		signal:                  make(chan struct{}, 1),
		handler:                 handler,
		streams:                 make(map[int]*stream),
		deadstreams:             make(map[int]struct{}),
		freestreams:             make(map[int]struct{}),
		nextnewstream:           1,
		pingResults:             make(map[uint64][]chan error),
		outgoingAcks:            make(map[int]int),
		outgoingCtrl:            make(map[int]struct{}),
		outgoingData:            make(map[int]struct{}),
		writeleft:               defaultConnWindowSize,
		localConnWindowSize:     defaultConnWindowSize,
		remoteConnWindowSize:    defaultConnWindowSize,
		localStreamWindowSize:   defaultStreamWindowSize,
		remoteStreamWindowSize:  defaultStreamWindowSize,
		localInactivityTimeout:  defaultInactivityTimeout,
		remoteInactivityTimeout: defaultInactivityTimeout,
	}
	if isserver {
		c.nextnewstream = 2
	} else {
		c.nextnewstream = 1
	}
	c.waitready.L = &c.lock
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

func (c *rawConn) closeWithErrorLocked(err error, outerr error) error {
	if !c.closed {
		c.closed = true
		c.failure = err
		c.outgoingFailure = outerr
		close(c.signal)
		c.waitready.Broadcast()
		return nil
	}
	return c.failure
}

func (c *rawConn) closeWithError(err error) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.closeWithErrorLocked(err, err)
}

func (c *rawConn) closeWithErrorAck(err error) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.closeWithErrorLocked(err, ECONNCLOSED)
}

func (c *rawConn) Wait() error {
	c.lock.Lock()
	defer c.lock.Unlock()
	for !c.closed {
		c.waitready.Wait()
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
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		return nil, c.failure
	}
	var streamID int
	for freeID := range c.freestreams {
		streamID = freeID
		delete(c.freestreams, freeID)
	}
	if streamID == 0 {
		streamID = c.nextnewstream
		if streamID >= 0x7ffffffe {
			return nil, ErrNoFreeStreamID
		}
		c.nextnewstream += 2
	}
	s := newOutgoingStream(c, streamID, target, c.remoteStreamWindowSize)
	c.streams[streamID] = s
	c.addOutgoingCtrlLocked(s.streamID)
	return s, nil
}

func (c *rawConn) addPingAck(value uint64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.pingAcks = append(c.pingAcks, value)
	c.wakeupLocked()
}

func (c *rawConn) takePingResult(value uint64) chan error {
	c.lock.Lock()
	defer c.lock.Unlock()
	if results, ok := c.pingResults[value]; ok {
		result := results[0]
		if len(results) > 1 {
			resultscopy := make([]chan error, len(results)-1)
			copy(resultscopy, results[1:])
			c.pingResults[value] = resultscopy
		} else {
			delete(c.pingResults, value)
		}
		return result
	}
	return nil
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

func (c *rawConn) addOutgoingAckLocked(streamID int, increment int) {
	if !c.closed && increment > 0 {
		wakeup := len(c.outgoingAcks) == 0
		c.outgoingAcks[streamID] += increment
		if wakeup {
			c.wakeupLocked()
		}
	}
}

func (c *rawConn) addOutgoingCtrlLocked(streamID int) {
	if !c.closed {
		wakeup := len(c.outgoingCtrl) == 0
		c.outgoingCtrl[streamID] = struct{}{}
		if wakeup {
			c.wakeupLocked()
		}
	}
}

func (c *rawConn) addOutgoingDataLocked(streamID int) {
	if !c.closed {
		wakeup := len(c.outgoingData) == 0 && c.writeleft > 0
		c.outgoingData[streamID] = struct{}{}
		if wakeup {
			c.wakeupLocked()
		}
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
		delete(c.outgoingAcks, s.streamID)
		delete(c.outgoingData, s.streamID)
		if isServerStreamID(s.streamID) == c.isserver {
			// this is our stream id, it is no longer in use, but must wait
			// for confirmation first before we may reuse it
			c.deadstreams[s.streamID] = struct{}{}
		}
	}
}

func (c *rawConn) processOpenFrame(frame openFrame) error {
	if frame.streamID <= 0 || isServerStreamID(frame.streamID) == c.isserver {
		return &errorWithReason{
			error:  fmt.Errorf("stream 0x%08x cannot be used for opening streams", frame.streamID),
			reason: EINVALIDSTREAM,
		}
	}
	if len(frame.data) > c.localStreamWindowSize {
		return &errorWithReason{
			error:  fmt.Errorf("stream 0x%08x initial %d bytes is more than %d bytes window", frame.streamID, len(frame.data), c.localStreamWindowSize),
			reason: EWINDOWOVERFLOW,
		}
	}
	s := newIncomingStream(c, frame, c.remoteStreamWindowSize)
	c.lock.Lock()
	defer c.lock.Unlock()
	old := c.streams[frame.streamID]
	if old != nil {
		return &errorWithReason{
			error:  fmt.Errorf("stream 0x%08x cannot be opened until fully closed", frame.streamID),
			reason: EINVALIDSTREAM,
		}
	}
	c.streams[frame.streamID] = s
	go c.handleStream(frame.targetID, s)
	if len(frame.data) > 0 {
		c.addOutgoingAckLocked(0, len(frame.data))
	}
	return nil
}

func (c *rawConn) processDataFrame(frame dataFrame) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	s := c.streams[frame.streamID]
	if s == nil {
		return &errorWithReason{
			error:  fmt.Errorf("stream 0x%08x cannot be found", frame.streamID),
			reason: EINVALIDSTREAM,
		}
	}
	err := s.processDataFrameLocked(frame)
	if err != nil {
		return err
	}
	c.cleanupStreamLocked(s)
	if len(frame.data) > 0 {
		c.addOutgoingAckLocked(0, len(frame.data))
	}
	return nil
}

func (c *rawConn) processResetFrame(frame resetFrame) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	s := c.streams[frame.streamID]
	if s != nil {
		err := s.processResetFrameLocked(frame)
		if err != nil {
			return err
		}
		c.cleanupStreamLocked(s)
	}
	return nil
}

func (c *rawConn) processWindowFrame(frame windowFrame) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	if frame.streamID == 0 {
		wakeup := c.writeleft == 0 && len(c.outgoingData) > 0
		c.writeleft += frame.increment
		if wakeup {
			c.wakeupLocked()
		}
		return nil
	}
	s := c.streams[frame.streamID]
	if s != nil {
		return s.processWindowFrameLocked(frame)
	}
	return nil
}

func (c *rawConn) failEverything() {
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
	streams := c.streams
	c.streams = nil
	for _, stream := range streams {
		stream.closeWithErrorLocked(c.failure)
	}
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
		case fatalFrame:
			c.closeWithErrorAck(frame.toError())
			break readloop
		case windowFrame:
			err = c.processWindowFrame(frame)
			if err != nil {
				c.closeWithError(err)
				break readloop
			}
		case settingsFrame:
			panic("unhandled settings frame")
		default:
			panic("unhandled frame type")
		}
	}
	c.failEverything()
}

func (c *rawConn) prepareWriteBatch(datarequired bool) (frames []frame, err error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		return nil, c.outgoingFailure
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
	if len(c.outgoingCtrl) > 0 {
		var sent int
		for streamID := range c.outgoingCtrl {
			s := c.streams[streamID]
			if s == nil {
				// the connection is already gone
				delete(c.outgoingCtrl, streamID)
				continue
			}
			frames, sent = s.outgoingFramesLocked(frames, c.writeleft)
			if !s.activeCtrl() {
				// should always be the case
				delete(c.outgoingCtrl, streamID)
			}
			if sent > 0 && !s.activeData() {
				// we sent all data together with control
				delete(c.outgoingData, streamID)
			}
			if len(frames) > 0 {
				c.writeleft -= sent
				return
			}
		}
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
			if !s.activeData() {
				delete(c.outgoingData, streamID)
			}
			if len(frames) > 0 {
				c.writeleft -= sent
				return
			}
		}
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
