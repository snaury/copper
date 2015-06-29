package copper

import (
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
	Sync() <-chan error
	Ping(value int64) <-chan error
	Open(target int64) (stream Stream, err error)
}

type rawConn struct {
	lock                    sync.Mutex
	waitready               sync.Cond
	conn                    net.Conn
	creader                 *connReader
	cwriter                 *connWriter
	isserver                bool
	closed                  bool
	failure                 error
	signal                  chan struct{}
	handler                 StreamHandler
	streams                 map[uint32]*rawStream
	deadstreams             map[uint32]struct{}
	freestreams             map[uint32]struct{}
	nextnewstream           uint32
	pingAcks                []int64
	pingQueue               []int64
	pingResults             map[int64][]chan<- error
	settingsAcks            int
	settingsCallbacks       []func(error)
	outgoingAcks            map[uint32]uint32
	outgoingCtrl            map[uint32]struct{}
	outgoingData            map[uint32]struct{}
	outgoingFailure         error
	writeleft               int
	localConnWindowSize     int
	remoteConnWindowSize    int
	localStreamWindowSize   int
	remoteStreamWindowSize  int
	localInactivityTimeout  time.Duration
	remoteInactivityTimeout time.Duration
	readblocked             int
	readunblocked           sync.Cond
	writeblocked            int
	writeunblocked          sync.Cond
}

var _ Conn = &rawConn{}

// NewConn wraps the underlying network connection with the copper protocol
func NewConn(conn net.Conn, handler StreamHandler, isserver bool) Conn {
	c := &rawConn{
		conn:                    conn,
		isserver:                isserver,
		closed:                  false,
		signal:                  make(chan struct{}, 1),
		handler:                 handler,
		streams:                 make(map[uint32]*rawStream),
		deadstreams:             make(map[uint32]struct{}),
		freestreams:             make(map[uint32]struct{}),
		nextnewstream:           1,
		pingResults:             make(map[int64][]chan<- error),
		outgoingAcks:            make(map[uint32]uint32),
		outgoingCtrl:            make(map[uint32]struct{}),
		outgoingData:            make(map[uint32]struct{}),
		writeleft:               defaultConnWindowSize,
		localConnWindowSize:     defaultConnWindowSize,
		remoteConnWindowSize:    defaultConnWindowSize,
		localStreamWindowSize:   defaultStreamWindowSize,
		remoteStreamWindowSize:  defaultStreamWindowSize,
		localInactivityTimeout:  defaultInactivityTimeout,
		remoteInactivityTimeout: defaultInactivityTimeout,
	}
	c.creader = newConnReader(c)
	c.cwriter = newConnWriter(c)
	if isserver {
		c.nextnewstream = 2
	} else {
		c.nextnewstream = 1
	}
	c.waitready.L = &c.lock
	c.readunblocked.L = &c.lock
	c.writeunblocked.L = &c.lock
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

func (c *rawConn) readFrame(scratch []byte) (rawFrame frame, err error) {
	rawFrame, err = readFrame(c.creader.buffer, scratch)
	if debugConnReadFrame {
		if err != nil {
			log.Printf("%s: read error: %v", c.debugPrefix(), err)
		} else {
			log.Printf("%s: read: %v", c.debugPrefix(), rawFrame)
		}
	}
	return
}

func (c *rawConn) writeFrameLocked(rawFrame frame) (err error) {
	if debugConnSendFrame {
		log.Printf("%s: send: %v", c.debugPrefix(), rawFrame)
	}
	err = rawFrame.writeFrameTo(c.cwriter.buffer)
	if debugConnSendFrame && err != nil {
		log.Printf("%s: send error: %v", c.debugPrefix(), err)
	}
	return
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
		if c.outgoingFailure == nil {
			c.outgoingFailure = err
		}
		close(c.signal)
		c.waitready.Broadcast()
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

func (c *rawConn) Sync() <-chan error {
	result := make(chan error, 1)
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		result <- c.failure
		close(result)
		return result
	}
	deadstreams := c.deadstreams
	c.deadstreams = make(map[uint32]struct{})
	go c.syncDeadStreams(deadstreams, result)
	return result
}

func (c *rawConn) syncDeadStreams(deadstreams map[uint32]struct{}, result chan<- error) {
	err := <-c.Ping(time.Now().UnixNano())
	if err == nil {
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
		err = c.failure
	}
	if result != nil {
		select {
		case result <- c.failure:
		default:
		}
		close(result)
	}
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

func (c *rawConn) Open(target int64) (Stream, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		return nil, c.failure
	}
	var streamID uint32
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
	stream := newOutgoingStream(c, streamID, target, c.localStreamWindowSize, c.remoteStreamWindowSize)
	c.streams[streamID] = stream
	c.addOutgoingCtrlLocked(stream.streamID)
	return stream, nil
}

func (c *rawConn) addOutgoingAckLocked(streamID uint32, increment int) {
	if !c.closed && increment > 0 {
		wakeup := len(c.outgoingAcks) == 0
		c.outgoingAcks[streamID] += uint32(increment)
		if wakeup {
			c.wakeupLocked()
		}
	}
}

func (c *rawConn) addOutgoingCtrlLocked(streamID uint32) {
	if !c.closed {
		wakeup := len(c.outgoingCtrl) == 0
		c.outgoingCtrl[streamID] = struct{}{}
		if wakeup {
			c.wakeupLocked()
		}
	}
}

func (c *rawConn) addOutgoingDataLocked(streamID uint32) {
	if !c.closed {
		if len(c.outgoingData) == 0 && c.writeleft > 0 {
			c.wakeupLocked()
		}
		c.outgoingData[streamID] = struct{}{}
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
		delete(c.outgoingCtrl, s.streamID)
		delete(c.outgoingData, s.streamID)
		if isServerStreamID(s.streamID) == c.isserver {
			// this is our stream id, it is no longer in use, but we must wait
			// for confirmation first before reusing it
			c.deadstreams[s.streamID] = struct{}{}
			if len(c.deadstreams) >= maxDeadStreams {
				deadstreams := c.deadstreams
				c.deadstreams = make(map[uint32]struct{})
				go c.syncDeadStreams(deadstreams, nil)
			}
		}
	}
}

func (c *rawConn) processPingFrameLocked(frame pingFrame) error {
	if (frame.flags & flagAck) == 0 {
		if len(c.pingAcks) == 0 {
			c.wakeupLocked()
		}
		c.pingAcks = append(c.pingAcks, frame.value)
	} else if results, ok := c.pingResults[frame.value]; ok {
		result := results[0]
		if len(results) > 1 {
			copy(results, results[1:])
			results[len(results)-1] = nil
			c.pingResults[frame.value] = results[:len(results)-1]
		} else {
			delete(c.pingResults, frame.value)
		}
		if result != nil {
			close(result)
		}
	}
	return nil
}

func (c *rawConn) processOpenFrameLocked(frame openFrame) error {
	if frame.streamID <= 0 || isServerStreamID(frame.streamID) == c.isserver {
		return &copperError{
			error: fmt.Errorf("stream 0x%08x cannot be used for opening streams", frame.streamID),
			code:  EINVALIDSTREAM,
		}
	}
	old := c.streams[frame.streamID]
	if old != nil {
		return &copperError{
			error: fmt.Errorf("stream 0x%08x cannot be reopened until fully closed", frame.streamID),
			code:  EINVALIDSTREAM,
		}
	}
	if len(frame.data) > c.localStreamWindowSize {
		return &copperError{
			error: fmt.Errorf("stream 0x%08x initial %d bytes, which is more than %d bytes window", frame.streamID, len(frame.data), c.localStreamWindowSize),
			code:  EWINDOWOVERFLOW,
		}
	}
	stream := newIncomingStream(c, frame, c.localStreamWindowSize, c.remoteStreamWindowSize)
	c.streams[frame.streamID] = stream
	go c.handleStream(stream)
	if len(frame.data) > 0 {
		c.addOutgoingAckLocked(0, len(frame.data))
	}
	return nil
}

func (c *rawConn) processDataFrameLocked(frame dataFrame) error {
	stream := c.streams[frame.streamID]
	if stream == nil {
		if frame.streamID == 0 {
			// this is a reserved stream id
			if frame.flags != 0 || len(frame.data) != 0 {
				return &copperError{
					error: fmt.Errorf("stream 0 cannot be used to send data"),
					code:  EINVALIDSTREAM,
				}
			}
			return nil
		}
		return &copperError{
			error: fmt.Errorf("stream 0x%08x cannot be found", frame.streamID),
			code:  EINVALIDSTREAM,
		}
	}
	err := stream.processDataFrameLocked(frame)
	if err != nil {
		return err
	}
	if len(frame.data) > 0 {
		c.addOutgoingAckLocked(0, len(frame.data))
	}
	return nil
}

func (c *rawConn) processResetFrameLocked(frame resetFrame) error {
	stream := c.streams[frame.streamID]
	if stream == nil {
		if frame.streamID == 0 {
			// this is a reserved stream id
			if c.outgoingFailure == nil {
				// send ECONNCLOSED unless other error is pending
				c.outgoingFailure = ECONNCLOSED
			}
			return frame.toError()
		}
		// it's ok to receive RESET for a dead stream
		return nil
	}
	err := stream.processResetFrameLocked(frame)
	if err != nil {
		return err
	}
	return nil
}

func (c *rawConn) processWindowFrameLocked(frame windowFrame) error {
	if frame.streamID == 0 {
		if frame.flags&flagInc != 0 {
			if len(c.outgoingData) > 0 && c.writeleft <= 0 {
				c.wakeupLocked()
			}
			c.writeleft += int(frame.increment)
		}
		return nil
	}
	stream := c.streams[frame.streamID]
	if stream != nil {
		return stream.processWindowFrameLocked(frame)
	}
	return nil
}

func (c *rawConn) processSettingsFrameLocked(frame settingsFrame) error {
	if frame.flags&flagAck != 0 {
		l := len(c.settingsCallbacks)
		if l > 0 {
			callback := c.settingsCallbacks[0]
			copy(c.settingsCallbacks, c.settingsCallbacks[1:])
			c.settingsCallbacks[l-1] = nil
			c.settingsCallbacks = c.settingsCallbacks[:l-1]
			callback(nil)
		}
		return nil
	}
	c.settingsAcks++
	for key, value := range frame.values {
		switch key {
		case settingsConnWindowID:
			if value < 1024 {
				return &copperError{
					error: fmt.Errorf("cannot set connection window to %d bytes", value),
					code:  EUNSUPPORTED,
				}
			}
			diff := value - c.remoteConnWindowSize
			c.writeleft += diff
			c.remoteConnWindowSize = value
		case settingsStreamWindowID:
			if value < 1024 {
				return &copperError{
					error: fmt.Errorf("cannot set stream window to %d bytes", value),
					code:  EUNSUPPORTED,
				}
			}
			diff := value - c.remoteStreamWindowSize
			for _, stream := range c.streams {
				stream.changeWindowLocked(diff)
			}
		case settingsInactivityMillisecondsID:
			if value < 1000 {
				return &copperError{
					error: fmt.Errorf("cannot set inactivity timeout to %dms", value),
					code:  EUNSUPPORTED,
				}
			}
			c.remoteInactivityTimeout = time.Duration(value) * time.Millisecond
		default:
			return &copperError{
				error: fmt.Errorf("unknown settings key %d", key),
				code:  EUNSUPPORTED,
			}
		}
	}
	c.wakeupLocked()
	return nil
}

func (c *rawConn) processFrame(rawFrame frame, scratch *[]byte) bool {
	c.lock.Lock()
	defer c.lock.Unlock()
	for c.readblocked > 0 {
		c.readunblocked.Wait()
	}

	var err error
	switch frame := rawFrame.(type) {
	case pingFrame:
		err = c.processPingFrameLocked(frame)
	case openFrame:
		err = c.processOpenFrameLocked(frame)
		if len(frame.data) > len(*scratch) {
			*scratch = frame.data
		}
	case dataFrame:
		err = c.processDataFrameLocked(frame)
		if len(frame.data) > len(*scratch) {
			*scratch = frame.data
		}
	case resetFrame:
		err = c.processResetFrameLocked(frame)
		if len(frame.message) > len(*scratch) {
			*scratch = frame.message
		}
	case windowFrame:
		err = c.processWindowFrameLocked(frame)
	case settingsFrame:
		err = c.processSettingsFrameLocked(frame)
	default:
		err = EUNKNOWNFRAME
	}
	if err != nil {
		c.closeWithErrorLocked(err)
		return false
	}
	return true
}

func (c *rawConn) failEverything() {
	c.lock.Lock()
	defer c.lock.Unlock()
	pingResults := c.pingResults
	c.pingQueue = nil
	c.pingResults = nil
	for _, results := range pingResults {
		for _, result := range results {
			if result != nil {
				select {
				case result <- c.failure:
				default:
				}
				close(result)
			}
		}
	}
	settingsCallbacks := c.settingsCallbacks
	c.settingsCallbacks = nil
	for _, callback := range settingsCallbacks {
		callback(c.failure)
	}
	streams := c.streams
	c.streams = nil
	for _, stream := range streams {
		stream.closeWithErrorLocked(c.failure)
	}
}

func (c *rawConn) readloop() {
	var scratch []byte
	for {
		rawFrame, err := c.readFrame(scratch)
		if err != nil {
			c.closeWithError(err)
			break
		}
		if !c.processFrame(rawFrame, &scratch) {
			break
		}
	}
	c.failEverything()
}

func (c *rawConn) streamCanReceive(streamID uint32) bool {
	if streamID == 0 {
		return true
	}
	stream := c.streams[streamID]
	if stream != nil {
		return stream.canReceive()
	}
	return false
}

func (c *rawConn) writeOutgoingFrames(datarequired bool, timeout *time.Duration) (result bool) {
	c.lock.Lock()
	defer c.lock.Unlock()
	for c.writeblocked > 0 {
		c.writeunblocked.Wait()
	}

	writesInitial := c.cwriter.writes
	defer func() {
		if result && datarequired && writesInitial == c.cwriter.writes && c.cwriter.buffer.Buffered() <= 0 {
			// we haven't written anything on the wire, but it is required
			err := c.writeFrameLocked(dataFrame{})
			if err != nil {
				c.closeWithErrorLocked(err)
				result = false
				return
			}
		}
		if c.cwriter.buffer.Buffered() > 0 {
			// we use buffer for coalescing, must flush at the end
			err := c.cwriter.buffer.Flush()
			if err != nil {
				c.closeWithErrorLocked(err)
				result = false
				return
			}
		}
		*timeout = c.remoteInactivityTimeout
	}()

writeloop:
	for {
		if c.closed {
			// Attempt to notify the other side that we have an error
			// It's ok if any of this fails, the read side will stop
			// when we close the connection.
			c.writeFrameLocked(errorToResetFrame(flagFin, 0, c.outgoingFailure))
			return false
		}
		writes := c.cwriter.writes
		if len(c.pingAcks) > 0 {
			pingAcks := c.pingAcks
			c.pingAcks = nil
			for _, value := range pingAcks {
				err := c.writeFrameLocked(pingFrame{
					flags: flagAck,
					value: value,
				})
				if err != nil {
					c.closeWithErrorLocked(err)
					return false
				}
			}
			if writes != c.cwriter.writes {
				// data flush detected, restart
				continue writeloop
			}
		}
		if len(c.pingQueue) > 0 {
			pingQueue := c.pingQueue
			c.pingQueue = nil
			for _, value := range pingQueue {
				err := c.writeFrameLocked(pingFrame{
					value: value,
				})
				if err != nil {
					c.closeWithErrorLocked(err)
					return false
				}
			}
			if writes != c.cwriter.writes {
				// data flush detected, restart
				continue writeloop
			}
		}
		if c.settingsAcks > 0 {
			c.settingsAcks--
			err := c.writeFrameLocked(settingsFrame{
				flags: flagAck,
			})
			if err != nil {
				c.closeWithErrorLocked(err)
				return false
			}
			if writes != c.cwriter.writes {
				// data flush detected, restart
				continue writeloop
			}
		}
		if len(c.outgoingAcks) > 0 {
			for streamID, increment := range c.outgoingAcks {
				delete(c.outgoingAcks, streamID)
				flags := flagAck
				if c.streamCanReceive(streamID) {
					flags |= flagInc
				}
				err := c.writeFrameLocked(windowFrame{
					streamID:  streamID,
					flags:     flags,
					increment: increment,
				})
				if err != nil {
					c.closeWithErrorLocked(err)
					return false
				}
				if writes != c.cwriter.writes {
					// data flush detected, restart
					continue writeloop
				}
			}
		}
		if len(c.outgoingCtrl) > 0 {
			for streamID := range c.outgoingCtrl {
				delete(c.outgoingCtrl, streamID)
				stream := c.streams[streamID]
				if stream != nil {
					err := stream.writeOutgoingCtrlLocked()
					if err != nil {
						c.closeWithErrorLocked(err)
						return false
					}
					if writes != c.cwriter.writes {
						// data flush detected, restart
						continue writeloop
					}
				}
			}
		}
		if len(c.outgoingData) > 0 && c.writeleft > 0 {
			for streamID := range c.outgoingData {
				delete(c.outgoingData, streamID)
				stream := c.streams[streamID]
				if stream != nil {
					err := stream.writeOutgoingDataLocked()
					if err != nil {
						c.closeWithErrorLocked(err)
						return false
					}
					if writes != c.cwriter.writes {
						// data flush detected, restart
						continue writeloop
					}
				}
			}
		}
		select {
		case <-c.signal:
			// the signal channel is active, must try again!
			continue writeloop
		default:
			// if we reach here there's nothing else to send
			return true
		}
	}
}

func (c *rawConn) writeloop() {
	defer c.conn.Close()
	timeout := c.remoteInactivityTimeout
	t := time.NewTimer(2 * timeout / 3)
	for {
		datarequired := false
		select {
		case <-c.signal:
			// we might have something to send
		case <-t.C:
			datarequired = true
		}
		writes := c.cwriter.writes
		newtimeout := timeout
		if !c.writeOutgoingFrames(datarequired, &newtimeout) {
			break
		}
		if writes != c.cwriter.writes || timeout != newtimeout {
			// data flush detected, reset the timer
			timeout = newtimeout
			t.Reset(2 * timeout / 3)
		}
	}
}

func (c *rawConn) blockRead() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.readblocked++
}

func (c *rawConn) blockWrite() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.writeblocked++
}

func (c *rawConn) unblockRead() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.readblocked--
	if c.readblocked == 0 {
		c.readunblocked.Broadcast()
	}
}

func (c *rawConn) unblockWrite() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.writeblocked--
	if c.writeblocked == 0 {
		c.writeunblocked.Broadcast()
	}
}
