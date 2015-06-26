package copper

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

const (
	debugConnReadFrame = false
	debugConnSendFrame = false
)

const (
	maxDeadStreams           = 4096
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
	Ping(value int64) <-chan error
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
	streams                 map[int]*rawStream
	deadstreams             map[int]struct{}
	freestreams             map[int]struct{}
	nextnewstream           int
	pingAcks                []int64
	pingQueue               []int64
	pingResults             map[int64][]chan error
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
		streams:                 make(map[int]*rawStream),
		deadstreams:             make(map[int]struct{}),
		freestreams:             make(map[int]struct{}),
		nextnewstream:           1,
		pingResults:             make(map[int64][]chan error),
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

func (c *rawConn) debugPrefix() string {
	if c.isserver {
		return "server"
	}
	return "client"
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

func (c *rawConn) Ping(value int64) <-chan error {
	result := make(chan error, 1)
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		result <- c.failure
		close(result)
		return result
	}
	if len(c.pingQueue) == 0 {
		c.wakeupLocked()
	}
	c.pingResults[value] = append(c.pingResults[value], result)
	c.pingQueue = append(c.pingQueue, value)
	return result
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
		break
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

func (c *rawConn) addPingAck(value int64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if len(c.pingAcks) == 0 {
		c.wakeupLocked()
	}
	c.pingAcks = append(c.pingAcks, value)
}

func (c *rawConn) takePingResult(value int64) chan error {
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

func (c *rawConn) doCausalConfirmation(deadstreams map[int]struct{}) {
	err := <-c.Ping(time.Now().UnixNano())
	if err != nil {
		return
	}
	// receiving a successful response to ping proves than all frames
	// before our outgoing ping have been processed by the other side
	// which means we have a proof dead streams are free for reuse
	c.lock.Lock()
	defer c.lock.Unlock()
	if !c.closed {
		for streamID := range deadstreams {
			c.freestreams[streamID] = struct{}{}
		}
	}
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

func (c *rawConn) clearOutgoingAckLocked(streamID int) {
	if !c.closed {
		delete(c.outgoingAcks, streamID)
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

func (c *rawConn) handleStream(stream Stream) {
	if c.handler == nil {
		// This connection does not support incoming streams
		stream.CloseWithError(ENOTARGET)
		return
	}
	defer stream.Close()
	c.handler.HandleStream(stream)
}

func (c *rawConn) cleanupStreamLocked(s *rawStream) {
	if s.isFullyClosed() {
		// connection is fully closed and must be forgotten
		delete(c.streams, s.streamID)
		delete(c.outgoingAcks, s.streamID)
		delete(c.outgoingCtrl, s.streamID)
		delete(c.outgoingData, s.streamID)
		if isServerStreamID(s.streamID) == c.isserver {
			// this is our stream id, it is no longer in use, but must wait
			// for confirmation first before we may reuse it
			c.deadstreams[s.streamID] = struct{}{}
			if len(c.deadstreams) >= maxDeadStreams {
				deadstreams := c.deadstreams
				c.deadstreams = make(map[int]struct{})
				go c.doCausalConfirmation(deadstreams)
			}
		}
	}
}

func (c *rawConn) processPingFrame(frame pingFrame) error {
	if (frame.flags & flagAck) == 0 {
		c.addPingAck(frame.value)
		return nil
	}
	result := c.takePingResult(frame.value)
	if result != nil {
		select {
		case result <- nil:
		default:
		}
		close(result)
	}
	return nil
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
			error:  fmt.Errorf("stream 0x%08x initial %d bytes, which is more than %d bytes window", frame.streamID, len(frame.data), c.localStreamWindowSize),
			reason: EWINDOWOVERFLOW,
		}
	}
	stream := newIncomingStream(c, frame, c.remoteStreamWindowSize)
	c.lock.Lock()
	defer c.lock.Unlock()
	old := c.streams[frame.streamID]
	if old != nil {
		return &errorWithReason{
			error:  fmt.Errorf("stream 0x%08x cannot be opened until fully closed", frame.streamID),
			reason: EINVALIDSTREAM,
		}
	}
	c.streams[frame.streamID] = stream
	go c.handleStream(stream)
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
		if frame.streamID == 0 {
			// this is a reserved stream id
			if frame.flags != 0 || len(frame.data) != 0 {
				return &errorWithReason{
					error:  fmt.Errorf("stream 0 cannot be used to send data"),
					reason: EINVALIDSTREAM,
				}
			}
			return nil
		}
		return &errorWithReason{
			error:  fmt.Errorf("stream 0x%08x cannot be found", frame.streamID),
			reason: EINVALIDSTREAM,
		}
	}
	if s.readbuf.len()+len(frame.data) > c.localStreamWindowSize {
		return &errorWithReason{
			error:  fmt.Errorf("stream 0x%08x received %d+%d bytes, which is more than %d bytes window", frame.streamID, s.readbuf.len(), len(frame.data), c.localStreamWindowSize),
			reason: EWINDOWOVERFLOW,
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
	if s == nil {
		if frame.streamID == 0 {
			// this is a reserved stream id
			return &errorWithReason{
				error:  fmt.Errorf("stream 0 cannot be reset"),
				reason: EINVALIDSTREAM,
			}
		}
		// it's ok to receive RESET for a dead stream
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
			if debugConnReadFrame {
				log.Printf("%s: read error: %v", c.debugPrefix(), err)
			}
			c.closeWithError(err)
			break readloop
		}
		if debugConnReadFrame {
			log.Printf("%s: read: %#v", c.debugPrefix(), rawFrame)
		}
		switch frame := rawFrame.(type) {
		case pingFrame:
			err = c.processPingFrame(frame)
		case openFrame:
			err = c.processOpenFrame(frame)
		case dataFrame:
			err = c.processDataFrame(frame)
		case resetFrame:
			err = c.processResetFrame(frame)
		case fatalFrame:
			c.closeWithErrorAck(frame.toError())
			break readloop
		case windowFrame:
			err = c.processWindowFrame(frame)
		case settingsFrame:
			panic("unhandled settings frame")
		default:
			panic("unhandled frame type")
		}
		if err != nil {
			c.closeWithError(err)
			break readloop
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
	if len(c.pingAcks) > 0 || len(c.pingQueue) > 0 {
		for _, value := range c.pingAcks {
			frames = append(frames, pingFrame{
				flags: flagAck,
				value: value,
			})
		}
		for _, value := range c.pingQueue {
			frames = append(frames, pingFrame{
				value: value,
			})
		}
		c.pingAcks = c.pingAcks[:0]
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
			if len(frames) > 0 {
				c.writeleft -= sent
				if sent > 0 && !s.activeData() {
					// we sent all data together with control
					delete(c.outgoingData, streamID)
				}
				c.cleanupStreamLocked(s)
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
				c.cleanupStreamLocked(s)
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
	defer c.conn.Close()
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
				frame := errorToFatalFrame(err)
				if debugConnSendFrame {
					log.Printf("%s: send: %#v", c.debugPrefix(), frame)
				}
				c.conn.SetWriteDeadline(time.Now().Add(c.localInactivityTimeout))
				err = frame.writeFrameTo(w)
				if debugConnSendFrame && err != nil {
					log.Printf("%s: send error: %v", c.debugPrefix(), err)
					return
				}
				err = w.Flush()
				if debugConnSendFrame && err != nil {
					log.Printf("%s: send error: %v", c.debugPrefix(), err)
				}
				return
			}
			// if there are no frames to send we may go to sleep
			datarequired = false
			if len(frames) == 0 {
				break
			}
			// send all frames that have been accumulated
			for _, frame := range frames {
				if debugConnSendFrame {
					log.Printf("%s: send: %#v", c.debugPrefix(), frame)
				}
				c.conn.SetWriteDeadline(time.Now().Add(c.localInactivityTimeout))
				err = frame.writeFrameTo(w)
				if err != nil {
					if debugConnSendFrame {
						log.Printf("%s: send error: %v", c.debugPrefix(), err)
					}
					c.closeWithError(err)
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
				if debugConnSendFrame {
					log.Printf("%s: send error: %v", c.debugPrefix(), err)
				}
				c.closeWithError(err)
				return
			}
		}
		t.Reset(2 * c.remoteInactivityTimeout / 3)
	}
}
