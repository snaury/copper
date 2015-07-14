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
	defaultConnBufferSize    = 4096
)

// RawConn is a multiplexed connection implementing the copper protocol
type RawConn interface {
	Err() error
	Close() error
	Done() <-chan struct{}
	Closed() <-chan struct{}
	Shutdown() <-chan struct{}
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
	Sync() <-chan error
	Ping(value int64) <-chan error
	Open(target int64) (stream Stream, err error)
}

type rawConn struct {
	lock                    sync.Mutex
	conn                    net.Conn
	creader                 *rawConnReader
	cwriter                 *rawConnWriter
	isserver                bool
	closed                  bool
	closedchan              chan struct{}
	finishchan              chan struct{}
	finishgroup             sync.WaitGroup
	shutdown                bool
	shutdownchan            chan struct{}
	shutdowngroup           sync.WaitGroup
	failure                 error
	handler                 Handler
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
	outgoingCtrl            map[*rawStream]struct{}
	outgoingData            map[*rawStream]struct{}
	outgoingFailure         error
	writeleft               int
	writeready              sync.Cond
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
	closeblocked            int
	closeunblocked          sync.Cond
}

var _ RawConn = &rawConn{}

// NewRawConn wraps the underlying network connection with the copper protocol
func NewRawConn(conn net.Conn, handler Handler, isserver bool) RawConn {
	c := &rawConn{
		conn:                    conn,
		isserver:                isserver,
		closedchan:              make(chan struct{}),
		handler:                 handler,
		streams:                 make(map[uint32]*rawStream),
		deadstreams:             make(map[uint32]struct{}),
		freestreams:             make(map[uint32]struct{}),
		pingResults:             make(map[int64][]chan<- error),
		outgoingAcks:            make(map[uint32]uint32),
		outgoingCtrl:            make(map[*rawStream]struct{}),
		outgoingData:            make(map[*rawStream]struct{}),
		writeleft:               defaultConnWindowSize,
		localConnWindowSize:     defaultConnWindowSize,
		remoteConnWindowSize:    defaultConnWindowSize,
		localStreamWindowSize:   defaultStreamWindowSize,
		remoteStreamWindowSize:  defaultStreamWindowSize,
		localInactivityTimeout:  defaultInactivityTimeout,
		remoteInactivityTimeout: defaultInactivityTimeout,
	}
	c.creader = newRawConnReader(c)
	c.cwriter = newRawConnWriter(c)
	if isserver {
		c.nextnewstream = 2
	} else {
		c.nextnewstream = 1
	}
	c.writeready.L = &c.lock
	c.readunblocked.L = &c.lock
	c.writeunblocked.L = &c.lock
	c.closeunblocked.L = &c.lock
	c.finishgroup.Add(2)
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
	c.writeready.Signal()
}

func (c *rawConn) closeWithErrorLocked(err error, closed bool) error {
	if err == nil {
		err = ECONNCLOSED
	} else if isTimeout(err) {
		err = &copperError{
			error: err,
			code:  ECONNTIMEOUT,
		}
	}
	preverror := c.failure
	if !c.closed {
		c.closed = true
		c.failure = err
		if c.outgoingFailure == nil {
			if err == ECONNCLOSED {
				c.outgoingFailure = ECONNSHUTDOWN
			} else {
				c.outgoingFailure = err
			}
		}
		// Don't send any pings that haven't been sent already
		c.pingQueue = nil
		close(c.closedchan)
		c.writeready.Broadcast()
	}
	for _, stream := range c.streams {
		stream.closeWithErrorLocked(err, closed)
	}
	return preverror
}

func (c *rawConn) closeWithError(err error) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.closeWithErrorLocked(err, true)
}

func (c *rawConn) Err() error {
	c.lock.Lock()
	err := c.failure
	c.lock.Unlock()
	return err
}

func (c *rawConn) Close() error {
	return c.closeWithError(ECONNCLOSED)
}

func (c *rawConn) Done() <-chan struct{} {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.finishchan == nil {
		c.finishchan = make(chan struct{})
		go func() {
			defer close(c.finishchan)
			c.finishgroup.Wait()
		}()
	}
	return c.finishchan
}

func (c *rawConn) Closed() <-chan struct{} {
	return c.closedchan
}

func (c *rawConn) Shutdown() <-chan struct{} {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.shutdownchan == nil {
		c.shutdownchan = make(chan struct{})
		go func() {
			defer close(c.shutdownchan)
			c.shutdowngroup.Wait()
		}()
	}
	return c.shutdownchan
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
	c.finishgroup.Add(1)
	go c.syncDeadStreams(deadstreams, result)
	return result
}

func (c *rawConn) syncDeadStreams(deadstreams map[uint32]struct{}, result chan<- error) {
	defer c.finishgroup.Done()
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
	c.pingResults[value] = append(c.pingResults[value], result)
	c.pingQueue = append(c.pingQueue, value)
	c.wakeupLocked()
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
		delete(c.freestreams, freeID)
		streamID = freeID
		break
	}
	if streamID == 0 {
		streamID = c.nextnewstream
		if streamID >= 0x7ffffffe {
			return nil, ErrNoFreeStreamID
		}
		c.nextnewstream += 2
	}
	return newOutgoingStream(c, streamID, target, c.localStreamWindowSize, c.remoteStreamWindowSize), nil
}

func (c *rawConn) addOutgoingAckLocked(streamID uint32, increment int) {
	if !c.closed && increment > 0 {
		c.outgoingAcks[streamID] += uint32(increment)
		c.wakeupLocked()
	}
}

func (c *rawConn) addOutgoingCtrlLocked(stream *rawStream) {
	if !c.closed {
		c.outgoingCtrl[stream] = struct{}{}
		c.wakeupLocked()
	}
}

func (c *rawConn) addOutgoingDataLocked(stream *rawStream) {
	if !c.closed {
		c.outgoingData[stream] = struct{}{}
		c.wakeupLocked()
	}
}

func (c *rawConn) handleStream(stream Stream) {
	defer c.finishgroup.Done()
	defer c.shutdowngroup.Done()
	if c.handler == nil {
		// This connection does not support incoming streams
		stream.CloseWithError(ENOTARGET)
		return
	}
	defer stream.Close()
	c.handler.Handle(stream)
}

func (c *rawConn) removeStreamLocked(stream *rawStream) {
	if c.streams[stream.streamID] == stream {
		// connection is fully closed and must be forgotten
		delete(c.streams, stream.streamID)
		delete(c.outgoingCtrl, stream)
		delete(c.outgoingData, stream)
		if isServerStreamID(stream.streamID) == c.isserver {
			// this is our stream id, it is no longer in use, but we must wait
			// for confirmation first before reusing it
			c.deadstreams[stream.streamID] = struct{}{}
			if len(c.deadstreams) >= maxDeadStreams {
				deadstreams := c.deadstreams
				c.deadstreams = make(map[uint32]struct{})
				c.finishgroup.Add(1)
				go c.syncDeadStreams(deadstreams, nil)
			}
		}
	}
}

func (c *rawConn) processPingFrameLocked(frame *pingFrame) error {
	if (frame.flags & flagAck) == 0 {
		c.pingAcks = append(c.pingAcks, frame.value)
		c.wakeupLocked()
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

func (c *rawConn) processOpenFrameLocked(frame *openFrame) error {
	if frame.streamID <= 0 || isServerStreamID(frame.streamID) == c.isserver {
		return copperError{
			error: fmt.Errorf("stream 0x%08x cannot be used for opening streams", frame.streamID),
			code:  EINVALIDSTREAM,
		}
	}
	old := c.streams[frame.streamID]
	if old != nil {
		return copperError{
			error: fmt.Errorf("stream 0x%08x cannot be reopened until fully closed", frame.streamID),
			code:  EINVALIDSTREAM,
		}
	}
	if len(frame.data) > c.localStreamWindowSize {
		return copperError{
			error: fmt.Errorf("stream 0x%08x initial %d bytes, which is more than %d bytes window", frame.streamID, len(frame.data), c.localStreamWindowSize),
			code:  EWINDOWOVERFLOW,
		}
	}
	stream := newIncomingStream(c, frame, c.localStreamWindowSize, c.remoteStreamWindowSize)
	if c.closed {
		// we are closed and ignore valid OPEN frames
		err := c.failure
		if err == nil {
			err = ECONNCLOSED
		}
		stream.closeWithErrorLocked(err, true)
		return nil
	}
	if c.shutdownchan != nil {
		// we are shutting down and should close incoming streams
		stream.closeWithErrorLocked(ECONNSHUTDOWN, true)
		return nil
	}
	c.finishgroup.Add(1)
	c.shutdowngroup.Add(1)
	go c.handleStream(stream)
	if len(frame.data) > 0 {
		c.addOutgoingAckLocked(0, len(frame.data))
	}
	return nil
}

func (c *rawConn) processDataFrameLocked(frame *dataFrame) error {
	stream := c.streams[frame.streamID]
	if stream == nil {
		if frame.streamID == 0 {
			// this is a reserved stream id
			if frame.flags != 0 || len(frame.data) != 0 {
				return copperError{
					error: fmt.Errorf("stream 0 cannot be used to send data"),
					code:  EINVALIDSTREAM,
				}
			}
			return nil
		}
		return copperError{
			error: fmt.Errorf("stream 0x%08x cannot be found", frame.streamID),
			code:  EINVALIDSTREAM,
		}
	}
	if c.closed {
		// we are closed and ignore valid DATA frames
		return nil
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

func (c *rawConn) processResetFrameLocked(frame *resetFrame) error {
	stream := c.streams[frame.streamID]
	if stream == nil {
		if frame.streamID == 0 {
			// this is a reserved stream id
			if c.outgoingFailure == nil {
				// send ECONNCLOSED unless other error is pending
				c.outgoingFailure = ECONNSHUTDOWN
			}
			return frame.err
		}
		// it's ok to receive RESET for a dead stream
		return nil
	}
	if c.closed {
		// we are closed and ignore valid RESET frames
		return nil
	}
	err := stream.processResetFrameLocked(frame)
	if err != nil {
		return err
	}
	return nil
}

func (c *rawConn) processWindowFrameLocked(frame *windowFrame) error {
	if frame.streamID == 0 {
		if frame.flags&flagInc != 0 {
			c.writeleft += int(frame.increment)
			c.wakeupLocked()
		}
		return nil
	}
	stream := c.streams[frame.streamID]
	if stream != nil {
		return stream.processWindowFrameLocked(frame)
	}
	return nil
}

func (c *rawConn) processSettingsFrameLocked(frame *settingsFrame) error {
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
				return copperError{
					error: fmt.Errorf("cannot set connection window to %d bytes", value),
					code:  EINVALIDFRAME,
				}
			}
			diff := value - c.remoteConnWindowSize
			c.writeleft += diff
			c.remoteConnWindowSize = value
		case settingsStreamWindowID:
			if value < 1024 {
				return copperError{
					error: fmt.Errorf("cannot set stream window to %d bytes", value),
					code:  EINVALIDFRAME,
				}
			}
			diff := value - c.remoteStreamWindowSize
			for _, stream := range c.streams {
				stream.changeWindowLocked(diff)
			}
		case settingsInactivityMillisecondsID:
			if value < 1000 {
				return copperError{
					error: fmt.Errorf("cannot set inactivity timeout to %dms", value),
					code:  EINVALIDFRAME,
				}
			}
			c.remoteInactivityTimeout = time.Duration(value) * time.Millisecond
		default:
			return copperError{
				error: fmt.Errorf("unknown settings key %d", key),
				code:  EINVALIDFRAME,
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
	case *pingFrame:
		err = c.processPingFrameLocked(frame)
	case *openFrame:
		err = c.processOpenFrameLocked(frame)
		if len(frame.data) > len(*scratch) {
			*scratch = frame.data
		}
	case *dataFrame:
		err = c.processDataFrameLocked(frame)
		if len(frame.data) > len(*scratch) {
			*scratch = frame.data
		}
	case *resetFrame:
		err = c.processResetFrameLocked(frame)
	case *windowFrame:
		err = c.processWindowFrameLocked(frame)
	case *settingsFrame:
		err = c.processSettingsFrameLocked(frame)
	default:
		err = EUNKNOWNFRAME
	}
	if err != nil {
		c.closeWithErrorLocked(err, false)
		return false
	}
	return true
}

func (c *rawConn) failEverything() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.pingQueue = nil
	for key, results := range c.pingResults {
		delete(c.pingResults, key)
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
	for streamID, stream := range c.streams {
		delete(c.streams, streamID)
		stream.closeWithErrorLocked(c.failure, false)
	}
}

func (c *rawConn) readloop() {
	defer c.finishgroup.Done()
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

func (c *rawConn) writeOutgoingFramesLocked() (result bool) {
	for c.writeblocked > 0 {
		c.writeunblocked.Wait()
	}

writeloop:
	for {
		writes := c.cwriter.writes
		if len(c.pingAcks) > 0 {
			pingAcks := c.pingAcks
			c.pingAcks = nil
			for _, value := range pingAcks {
				err := c.writeFrameLocked(&pingFrame{
					flags: flagAck,
					value: value,
				})
				if err != nil {
					c.closeWithErrorLocked(err, false)
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
				err := c.writeFrameLocked(&pingFrame{
					value: value,
				})
				if err != nil {
					c.closeWithErrorLocked(err, false)
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
			err := c.writeFrameLocked(&settingsFrame{
				flags: flagAck,
			})
			if err != nil {
				c.closeWithErrorLocked(err, false)
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
				err := c.writeFrameLocked(&windowFrame{
					streamID:  streamID,
					flags:     flags,
					increment: increment,
				})
				if err != nil {
					c.closeWithErrorLocked(err, false)
					return false
				}
				if writes != c.cwriter.writes {
					// data flush detected, restart
					continue writeloop
				}
			}
		}
		if c.closed {
			// Attempt to notify the other side that we have an error
			// It's ok if any of this fails, the read side will stop
			// when we close the connection.
			c.writeFrameLocked(&resetFrame{
				streamID: 0,
				flags:    flagFin,
				err:      c.outgoingFailure,
			})
			return false
		}
		if len(c.outgoingCtrl) > 0 {
			for stream := range c.outgoingCtrl {
				delete(c.outgoingCtrl, stream)
				err := stream.writeOutgoingCtrlLocked()
				if err != nil {
					c.closeWithErrorLocked(err, false)
					return false
				}
				if writes != c.cwriter.writes {
					// data flush detected, restart
					continue writeloop
				}
			}
		}
		if len(c.outgoingData) > 0 && c.writeleft > 0 {
			for stream := range c.outgoingData {
				delete(c.outgoingData, stream)
				err := stream.writeOutgoingDataLocked()
				if err != nil {
					c.closeWithErrorLocked(err, false)
					return false
				}
				if writes != c.cwriter.writes {
					// data flush detected, restart
					continue writeloop
				}
			}
		}
		// if we reach here there's nothing to write
		return true
	}
}

func (c *rawConn) writeloop() {
	defer c.finishgroup.Done()
	defer c.conn.Close()

	c.lock.Lock()
	defer c.lock.Unlock()

	var datarequired int
	timeout := c.remoteInactivityTimeout
	t := time.AfterFunc(2*timeout/3, func() {
		c.lock.Lock()
		defer c.lock.Unlock()
		datarequired++
		c.writeready.Signal()
	})
	defer t.Stop()
	for {
		writesAtStart := c.cwriter.writes
		ok := c.writeOutgoingFramesLocked()
		writesAtEnd := c.cwriter.writes
		if ok && datarequired > 0 {
			if writesAtStart == c.cwriter.writes && c.cwriter.buffer.Buffered() == 0 {
				// We haven't written anything on the wire, but it is required
				err := c.writeFrameLocked(&dataFrame{})
				if err != nil {
					c.closeWithErrorLocked(err, false)
					break
				}
			}
			datarequired = 0
		}
		if c.cwriter.buffer.Buffered() > 0 {
			err := c.cwriter.buffer.Flush()
			if err != nil {
				c.closeWithErrorLocked(err, false)
				break
			}
		}
		if !ok {
			break
		}
		if writesAtStart != c.cwriter.writes || timeout != c.remoteInactivityTimeout {
			// Either we have written some data on the wire, which implies we
			// have dropped the lock for some time, or inactivity timeout has
			// changed and we need to restart the timer.
			if writesAtStart == c.cwriter.writes {
				// Change of inactivity timeout when we don't have any data to
				// write is dangerous, since we don't know how much time in
				// current timer has elapsed and might miss a deadline. Make
				// sure to write some data as soon as possible.
				datarequired++
			}
			timeout = c.remoteInactivityTimeout
			t.Reset(2 * timeout / 3)
		}
		if writesAtEnd != c.cwriter.writes || datarequired > 0 {
			// When writeOutgoingFramesLocked exits all our queues are drained
			// However if flush unlocked the lock we have to check them again
			continue
		}
		// Wait until we have something to write
		c.writeready.Wait()
	}
	for c.closeblocked > 0 {
		c.closeunblocked.Wait()
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

func (c *rawConn) blockClose() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.closeblocked++
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

func (c *rawConn) unblockClose() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.closeblocked--
	if c.closeblocked == 0 {
		c.closeunblocked.Broadcast()
	}
}
