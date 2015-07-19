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
	Ping(value int64) <-chan error
	NewStream() (stream Stream, err error)
}

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

type rawConnStreams struct {
	owner  *rawConn
	err    error
	flag   uint32
	next   uint32
	closed bool

	live map[uint32]*rawStream
}

func (s *rawConnStreams) init(owner *rawConn, server bool) {
	s.owner = owner
	if server {
		s.flag = 0
		s.next = 2
	} else {
		s.flag = 1
		s.next = 1
	}
	s.live = make(map[uint32]*rawStream)
}

func (s *rawConnStreams) isClient() bool {
	return s.flag != 0
}

func (s *rawConnStreams) ownedID(id uint32) bool {
	return id&1 == s.flag
}

func (s *rawConnStreams) failLocked(err error, closed bool) {
	if s.err == nil || closed && !s.closed {
		s.err = err
		if closed {
			s.closed = true
		}
		for _, stream := range s.live {
			s.owner.mu.Unlock()
			stream.closeWithError(err, closed)
			s.owner.mu.Lock()
		}
	}
}

func (s *rawConnStreams) allocateLocked() (uint32, error) {
	if s.err != nil {
		return 0, s.err
	}
	wrapped := false
	for {
		streamID := s.next
		s.next += 2
		if s.next >= 0x80000000 {
			if wrapped {
				return 0, ErrNoFreeStreamID
			}
			s.next -= 0x80000000
			if s.next == 0 {
				s.next = 2
			}
			wrapped = true
		}
		stream := s.live[streamID]
		if stream == nil {
			return streamID, nil
		}
	}
}

func (s *rawConnStreams) addLockedWithUnlock(stream *rawStream) {
	s.live[stream.streamID] = stream
	err := s.err
	closed := s.closed
	s.owner.mu.Unlock()
	if err != nil {
		stream.closeWithError(err, closed)
	}
}

func (s *rawConnStreams) find(streamID uint32) *rawStream {
	s.owner.mu.RLock()
	stream := s.live[streamID]
	s.owner.mu.RUnlock()
	return stream
}

func (s *rawConnStreams) remove(stream *rawStream) {
	s.owner.mu.Lock()
	if s.live[stream.streamID] == stream {
		delete(s.live, stream.streamID)
	}
	s.owner.mu.Unlock()
}

func (s *rawConnStreams) changeWindow(diff int) {
	s.owner.mu.RLock()
	for _, stream := range s.live {
		stream.changeWindow(diff)
	}
	s.owner.mu.RUnlock()
}

type rawConnSettings struct {
	owner     *rawConn
	callbacks []func(error)

	localConnWindowSize     int
	remoteConnWindowSize    int
	localStreamWindowSize   int
	remoteStreamWindowSize  int
	localInactivityTimeout  time.Duration
	remoteInactivityTimeout time.Duration
}

func (s *rawConnSettings) init(owner *rawConn) {
	s.owner = owner
	s.localConnWindowSize = defaultConnWindowSize
	s.remoteConnWindowSize = defaultConnWindowSize
	s.localStreamWindowSize = defaultStreamWindowSize
	s.remoteStreamWindowSize = defaultStreamWindowSize
	s.localInactivityTimeout = defaultInactivityTimeout
	s.remoteInactivityTimeout = defaultInactivityTimeout
}

func (s *rawConnSettings) failLocked(err error) {
	callbacks := s.callbacks
	s.callbacks = nil
	for _, callback := range callbacks {
		if callback != nil {
			callback(err)
		}
	}
}

func (s *rawConnSettings) handleAck() {
	var callback func(error)
	s.owner.mu.Lock()
	if len(s.callbacks) > 0 {
		callback = s.callbacks[0]
		copy(s.callbacks, s.callbacks[1:])
		s.callbacks[len(s.callbacks)-1] = nil
		s.callbacks = s.callbacks[:len(s.callbacks)-1]
	}
	s.owner.mu.Unlock()
	if callback != nil {
		callback(nil)
	}
}

func (s *rawConnSettings) handleSettings(frame *settingsFrame) error {
	s.owner.mu.Lock()
	for key, value := range frame.values {
		switch key {
		case settingConnWindow:
			if value < minWindowSize || value > maxWindowSize {
				s.owner.mu.Unlock()
				return copperError{
					error: fmt.Errorf("cannot set connection window to %d bytes", value),
					code:  EINVALIDFRAME,
				}
			}
			diff := int(value) - s.remoteConnWindowSize
			s.owner.outgoing.changeWindow(diff)
			s.remoteConnWindowSize = int(value)
		case settingStreamWindow:
			if value < minWindowSize || value > maxWindowSize {
				s.owner.mu.Unlock()
				return copperError{
					error: fmt.Errorf("cannot set stream window to %d bytes", value),
					code:  EINVALIDFRAME,
				}
			}
			diff := int(value) - s.remoteStreamWindowSize
			s.owner.streams.changeWindow(diff)
			s.remoteStreamWindowSize = int(value)
		case settingInactivityMilliseconds:
			if value < 1000 {
				s.owner.mu.Unlock()
				return copperError{
					error: fmt.Errorf("cannot set inactivity timeout to %dms", value),
					code:  EINVALIDFRAME,
				}
			}
			s.remoteInactivityTimeout = time.Duration(value) * time.Millisecond
		default:
			s.owner.mu.Unlock()
			return copperError{
				error: fmt.Errorf("unknown settings key %d", key),
				code:  EINVALIDFRAME,
			}
		}
	}
	s.owner.mu.Unlock()
	s.owner.outgoing.addSettingsAck()
	return nil
}

func (s *rawConnSettings) getLocalStreamWindowSize() int {
	s.owner.mu.RLock()
	window := s.localStreamWindowSize
	s.owner.mu.RUnlock()
	return window
}

func (s *rawConnSettings) getRemoteStreamWindowSize() int {
	s.owner.mu.RLock()
	window := s.remoteStreamWindowSize
	s.owner.mu.RUnlock()
	return window
}

func (s *rawConnSettings) getLocalInactivityTimeout() time.Duration {
	s.owner.mu.RLock()
	d := s.localInactivityTimeout
	s.owner.mu.RUnlock()
	return d
}

func (s *rawConnSettings) getRemoteInactivityTimeout() time.Duration {
	s.owner.mu.RLock()
	d := s.remoteInactivityTimeout
	s.owner.mu.RUnlock()
	return d
}

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

type rawConn struct {
	mu            sync.RWMutex
	conn          net.Conn
	handler       Handler
	failure       error
	closedchan    chan struct{}
	finishchan    chan struct{}
	finishgroup   sync.WaitGroup
	shutdown      bool
	shutdownchan  chan struct{}
	shutdowngroup sync.WaitGroup

	pings    rawConnPings
	streams  rawConnStreams
	settings rawConnSettings
	outgoing rawConnOutgoing

	// only accessible from the readloop
	reader   *bufio.Reader
	lastread time.Time

	// only accessible from the writeloop
	writer    *bufio.Writer
	lastwrite time.Time
}

var _ RawConn = &rawConn{}

// NewRawConn wraps the underlying network connection with the copper protocol
func NewRawConn(conn net.Conn, handler Handler, server bool) RawConn {
	c := &rawConn{
		conn:       conn,
		handler:    handler,
		closedchan: make(chan struct{}),
	}

	c.pings.init(c)
	c.streams.init(c, server)
	c.settings.init(c)
	c.outgoing.init(c)

	c.reader = bufio.NewReaderSize(newRawConnReader(c), defaultConnBufferSize)
	c.lastread = time.Now()
	c.writer = bufio.NewWriterSize(newRawConnWriter(c), defaultConnBufferSize)
	c.lastwrite = time.Now()

	c.finishgroup.Add(2)
	go c.readloop()
	go c.writeloop()
	return c
}

var debugPrefixByClientFlag = map[bool]string{
	true:  "client",
	false: "server",
}

func (c *rawConn) debugPrefix() string {
	return debugPrefixByClientFlag[c.streams.isClient()]
}

func (c *rawConn) readFrame(scratch []byte) (rawFrame frame, err error) {
	rawFrame, err = readFrame(c.reader, scratch)
	if debugConnReadFrame {
		if err != nil {
			log.Printf("%s: read error: %v", c.debugPrefix(), err)
		} else {
			log.Printf("%s: read: %v", c.debugPrefix(), rawFrame)
		}
	}
	return
}

func (c *rawConn) writeFrame(rawFrame frame) (err error) {
	if debugConnSendFrame {
		log.Printf("%s: send: %v", c.debugPrefix(), rawFrame)
	}
	err = rawFrame.writeFrameTo(c.writer)
	if debugConnSendFrame && err != nil {
		log.Printf("%s: send error: %v", c.debugPrefix(), err)
	}
	return
}

func (c *rawConn) closeWithError(err error, closed bool) error {
	if err == nil {
		err = ECONNCLOSED
	} else if isTimeout(err) {
		err = &copperError{
			error: err,
			code:  ECONNTIMEOUT,
		}
	}
	c.mu.Lock()
	preverror := c.failure
	if c.failure == nil {
		c.failure = err
		close(c.closedchan)
		c.outgoing.fail(err)
	}
	c.streams.failLocked(err, closed)
	c.mu.Unlock()
	return preverror
}

func (c *rawConn) Err() error {
	c.mu.RLock()
	err := c.failure
	c.mu.RUnlock()
	return err
}

func (c *rawConn) Close() error {
	return c.closeWithError(ECONNCLOSED, true)
}

func (c *rawConn) Done() <-chan struct{} {
	c.mu.Lock()
	if c.finishchan == nil {
		c.finishchan = make(chan struct{})
		go func() {
			defer close(c.finishchan)
			c.finishgroup.Wait()
		}()
	}
	c.mu.Unlock()
	return c.finishchan
}

func (c *rawConn) Closed() <-chan struct{} {
	return c.closedchan
}

func (c *rawConn) Shutdown() <-chan struct{} {
	c.mu.Lock()
	if c.shutdownchan == nil {
		c.shutdownchan = make(chan struct{})
		go func() {
			defer close(c.shutdownchan)
			c.shutdowngroup.Wait()
		}()
	}
	c.mu.Unlock()
	return c.shutdownchan
}

func (c *rawConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *rawConn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *rawConn) Ping(value int64) <-chan error {
	result := make(chan error, 1)
	c.ping(value, func(err error) {
		result <- err
		close(result)
	})
	return result
}

func (c *rawConn) ping(value int64, callback func(err error)) {
	err := c.pings.addPing(value, callback)
	if err != nil {
		callback(err)
	}
}

func (c *rawConn) NewStream() (Stream, error) {
	c.mu.Lock()
	streamID, err := c.streams.allocateLocked()
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	return newOutgoingStreamWithUnlock(c, streamID), nil
}

func (c *rawConn) handleStream(handler Handler, stream Stream) {
	defer c.finishgroup.Done()
	defer c.shutdowngroup.Done()
	if handler == nil {
		// This connection does not support incoming streams
		stream.CloseWithError(ENOTARGET)
		return
	}
	defer stream.Close()
	handler.ServeCopper(stream)
}

func (c *rawConn) processPingFrame(frame *pingFrame) error {
	if (frame.flags & flagPingAck) != 0 {
		c.pings.handleAck(frame.value)
	} else {
		c.pings.handlePing(frame.value)
	}
	return nil
}

func (c *rawConn) processDataFrame(frame *dataFrame) error {
	if frame.streamID == 0 {
		if frame.flags != 0 || len(frame.data) != 0 {
			return copperError{
				error: fmt.Errorf("stream 0 cannot be used for data"),
				code:  EINVALIDSTREAM,
			}
		}
		return nil
	}
	stream := c.streams.find(frame.streamID)
	if frame.flags&flagDataOpen != 0 {
		// This frame is starting a new stream
		if c.streams.ownedID(frame.streamID) {
			return copperError{
				error: fmt.Errorf("stream 0x%08x cannot be used for opening streams", frame.streamID),
				code:  EINVALIDSTREAM,
			}
		}
		if stream != nil {
			return copperError{
				error: fmt.Errorf("stream 0x%08x cannot be reopened until fully closed", frame.streamID),
				code:  EINVALIDSTREAM,
			}
		}
		c.mu.Lock()
		window := c.settings.localStreamWindowSize
		if len(frame.data) > window {
			c.mu.Unlock()
			return copperError{
				error: fmt.Errorf("stream 0x%08x initial %d bytes, which is more than %d bytes window", frame.streamID, len(frame.data), window),
				code:  EWINDOWOVERFLOW,
			}
		}
		stream = newIncomingStreamWithUnlock(c, frame.streamID)
		c.mu.Lock()
		if c.failure != nil {
			// we are closed and ignore valid DATA frames
			err := c.failure
			c.mu.Unlock()
			stream.closeWithError(err, true)
			return nil
		}
		if c.shutdownchan != nil {
			// we are shutting down and should close incoming streams
			c.mu.Unlock()
			stream.closeWithError(ECONNSHUTDOWN, true)
			return nil
		}
		c.finishgroup.Add(1)
		c.shutdowngroup.Add(1)
		go c.handleStream(c.handler, stream)
		c.mu.Unlock()
	} else {
		if stream == nil {
			return copperError{
				error: fmt.Errorf("stream 0x%08x cannot be found", frame.streamID),
				code:  EINVALIDSTREAM,
			}
		}
		c.mu.RLock()
		if c.failure != nil {
			// we are closed and ignore valid DATA frames
			c.mu.RUnlock()
			return nil
		}
		c.mu.RUnlock()
	}
	err := stream.processDataFrame(frame)
	if err != nil {
		return err
	}
	if len(frame.data) > 0 {
		c.outgoing.incrementRemote(len(frame.data))
	}
	return nil
}

func (c *rawConn) processResetFrame(frame *resetFrame) error {
	if frame.streamID == 0 {
		// send ECONNCLOSED unless other error is pending
		c.outgoing.fail(ECONNSHUTDOWN)
		return frame.err
	}
	stream := c.streams.find(frame.streamID)
	if stream == nil {
		// it's ok to receive RESET for a dead stream
		return nil
	}
	c.mu.RLock()
	if c.failure != nil {
		// we are closed and ignore valid RESET frames
		c.mu.RUnlock()
		return nil
	}
	c.mu.RUnlock()
	err := stream.processResetFrame(frame)
	if err != nil {
		return err
	}
	return nil
}

func (c *rawConn) processWindowFrame(frame *windowFrame) error {
	if frame.streamID == 0 {
		c.outgoing.changeWindow(int(frame.increment))
		return nil
	}
	stream := c.streams.find(frame.streamID)
	if stream == nil {
		// it's ok to receive WINDOW for a dead stream
		return nil
	}
	return stream.processWindowFrame(frame)
}

func (c *rawConn) processSettingsFrame(frame *settingsFrame) error {
	if frame.flags&flagSettingsAck != 0 {
		c.settings.handleAck()
		return nil
	}
	return c.settings.handleSettings(frame)
}

func (c *rawConn) processFrame(rawFrame frame, scratch *[]byte) bool {
	var err error
	switch frame := rawFrame.(type) {
	case *pingFrame:
		err = c.processPingFrame(frame)
	case *dataFrame:
		err = c.processDataFrame(frame)
		if len(frame.data) > len(*scratch) {
			*scratch = frame.data
		}
	case *resetFrame:
		err = c.processResetFrame(frame)
	case *windowFrame:
		err = c.processWindowFrame(frame)
	case *settingsFrame:
		err = c.processSettingsFrame(frame)
	default:
		err = EUNKNOWNFRAME
	}
	if err != nil {
		c.closeWithError(err, false)
		return false
	}
	return true
}

func (c *rawConn) readloop() {
	defer c.finishgroup.Done()
	var scratch []byte
	for {
		rawFrame, err := c.readFrame(scratch)
		if err != nil {
			c.closeWithError(err, false)
			break
		}
		if !c.processFrame(rawFrame, &scratch) {
			break
		}
	}
	err := c.Err()
	c.pings.fail(err)
	c.mu.Lock()
	c.streams.failLocked(err, false)
	c.settings.failLocked(err)
	c.mu.Unlock()
}

func (c *rawConn) streamCanReceive(streamID uint32) bool {
	if streamID == 0 {
		return true
	}
	stream := c.streams.find(streamID)
	if stream != nil {
		return stream.canReceive()
	}
	return false
}

func (c *rawConn) writeOutgoingFrames() (result bool) {
	c.outgoing.mu.Lock()
	for c.outgoing.blocked > 0 {
		c.outgoing.unblocked.Wait()
	}

writeloop:
	for {
		pingAcks := c.outgoing.pingAcks
		if len(pingAcks) > 0 {
			c.outgoing.pingAcks = nil
			c.outgoing.mu.Unlock()
			for _, value := range pingAcks {
				err := c.writeFrame(&pingFrame{
					flags: flagPingAck,
					value: value,
				})
				if err != nil {
					c.closeWithError(err, false)
					return false
				}
			}
			c.outgoing.mu.Lock()
			continue writeloop
		}
		pingQueue := c.outgoing.pingQueue
		if len(pingQueue) > 0 {
			c.outgoing.pingQueue = nil
			c.outgoing.mu.Unlock()
			for _, value := range pingQueue {
				err := c.writeFrame(&pingFrame{
					value: value,
				})
				if err != nil {
					c.closeWithError(err, false)
					return false
				}
			}
			c.outgoing.mu.Lock()
			continue writeloop
		}
		if c.outgoing.settingsAcks > 0 {
			c.outgoing.settingsAcks--
			c.outgoing.mu.Unlock()
			err := c.writeFrame(&settingsFrame{
				flags: flagSettingsAck,
			})
			if err != nil {
				c.closeWithError(err, false)
				return false
			}
			c.outgoing.mu.Lock()
			continue writeloop
		}
		if c.outgoing.remoteIncrement > 0 {
			increment := c.outgoing.remoteIncrement
			c.outgoing.remoteIncrement = 0
			c.outgoing.mu.Unlock()
			err := c.writeFrame(&windowFrame{
				streamID:  0,
				increment: uint32(increment),
			})
			if err != nil {
				c.closeWithError(err, false)
				return false
			}
			c.outgoing.mu.Lock()
			continue writeloop
		}
		if c.outgoing.ctrl.size > 0 {
			stream := c.outgoing.ctrl.take()
			stream.inctrl = false
			c.outgoing.mu.Unlock()
			err := stream.writeCtrl()
			if err != nil {
				c.closeWithError(err, false)
				return false
			}
			c.outgoing.mu.Lock()
			continue writeloop
		}
		if c.outgoing.failure != nil {
			failure := c.outgoing.failure
			c.outgoing.mu.Unlock()
			// Attempt to notify the other side that we have an error
			// It's ok if any of this fails, the read side will stop
			// when we close the connection.
			c.writeFrame(&resetFrame{
				streamID: 0,
				flags:    0,
				err:      failure,
			})
			return false
		}
		if c.outgoing.data.size > 0 && c.outgoing.writeleft > 0 {
			stream := c.outgoing.data.take()
			stream.indata = false
			c.outgoing.mu.Unlock()
			err := stream.writeData()
			if err != nil {
				c.closeWithError(err, false)
				return false
			}
			c.outgoing.mu.Lock()
			continue writeloop
		}
		break
	}
	// if we reach here there's nothing to write
	c.outgoing.writeready = false
	c.outgoing.mu.Unlock()
	return true
}

func (c *rawConn) writeloop() {
	defer c.finishgroup.Done()
	defer c.conn.Close()

	lastwrite := c.lastwrite
	deadline := lastwrite.Add(c.settings.getRemoteInactivityTimeout() * 2 / 3)
	t := time.AfterFunc(deadline.Sub(time.Now()), func() {
		c.outgoing.mu.Lock()
		c.outgoing.wakeupLocked()
		c.outgoing.mu.Unlock()
	})
	defer t.Stop()
	for {
		c.outgoing.wait()
		ok := c.writeOutgoingFrames()
		newlastwrite := c.lastwrite
		if ok && lastwrite == newlastwrite && c.writer.Buffered() == 0 {
			// We haven't written anything for a while, write an empty frame
			err := c.writeFrame(&dataFrame{})
			if err != nil {
				c.closeWithError(err, false)
				break
			}
		}
		if c.writer.Buffered() > 0 {
			err := c.writer.Flush()
			if err != nil {
				c.closeWithError(err, false)
				break
			}
		}
		if !ok {
			break
		}
		newlastwrite = c.lastwrite
		newdeadline := newlastwrite.Add(c.settings.getRemoteInactivityTimeout() * 2 / 3)
		if newdeadline != deadline {
			lastwrite = newlastwrite
			deadline = newdeadline
			t.Reset(deadline.Sub(time.Now()))
		}
	}
}

func (c *rawConn) blockWrite() {
	c.outgoing.mu.Lock()
	c.outgoing.blocked++
	c.outgoing.mu.Unlock()
}

func (c *rawConn) unblockWrite() {
	c.outgoing.mu.Lock()
	c.outgoing.blocked--
	if c.outgoing.blocked == 0 {
		c.outgoing.unblocked.Broadcast()
	}
	c.outgoing.mu.Unlock()
}
