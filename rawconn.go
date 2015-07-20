package copper

import (
	"bufio"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	rawConnBufferSize = 4096
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
	Ping(data PingData) <-chan error
	NewStream() (stream Stream, err error)
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
	br       *bufio.Reader
	reader   *FrameReader
	lastread time.Time

	// only accessible from the writeloop
	bw        *bufio.Writer
	writer    *FrameWriter
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

	c.br = bufio.NewReaderSize(newRawConnReader(c), rawConnBufferSize)
	c.reader = NewFrameReader(c.br)
	c.lastread = time.Now()
	c.bw = bufio.NewWriterSize(newRawConnWriter(c), rawConnBufferSize)
	c.writer = NewFrameWriter(c.bw)
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

func (c *rawConn) closeWithError(err error) error {
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
		c.outgoing.clearPingQueue()
		c.streams.failLocked(err)
	}
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
	return c.closeWithError(ECONNCLOSED)
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

func (c *rawConn) Ping(data PingData) <-chan error {
	result := make(chan error, 1)
	c.ping(data, func(err error) {
		result <- err
		close(result)
	})
	return result
}

func (c *rawConn) ping(data PingData, callback func(err error)) {
	err := c.pings.addPing(data, callback)
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
	stream := newOutgoingStream(c, streamID)
	c.mu.Unlock()
	return stream, nil
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
	err := handler.ServeCopper(stream)
	if err != nil {
		stream.CloseWithError(err)
	}
}

func (c *rawConn) processPingFrame(frame *PingFrame) error {
	if frame.Flags.Has(FlagPingAck) {
		c.pings.handleAck(frame.Data)
	} else {
		c.pings.handlePing(frame.Data)
	}
	return nil
}

func (c *rawConn) processDataFrame(frame *DataFrame) error {
	if frame.StreamID == 0 {
		if frame.Flags != 0 || len(frame.Data) != 0 {
			return copperError{
				error: fmt.Errorf("stream 0 cannot be used for data"),
				code:  EINVALIDSTREAM,
			}
		}
		return nil
	}
	stream := c.streams.find(frame.StreamID)
	if frame.Flags.Has(FlagDataOpen) {
		// This frame is starting a new stream
		if c.streams.isOwnedID(frame.StreamID) {
			return copperError{
				error: fmt.Errorf("stream 0x%08x cannot be used for opening streams", frame.StreamID),
				code:  EINVALIDSTREAM,
			}
		}
		if stream != nil {
			return copperError{
				error: fmt.Errorf("stream 0x%08x cannot be reopened until fully closed", frame.StreamID),
				code:  EINVALIDSTREAM,
			}
		}
		c.mu.Lock()
		window := c.settings.localStreamWindowSize
		if len(frame.Data) > window {
			c.mu.Unlock()
			return copperError{
				error: fmt.Errorf("stream 0x%08x initial %d bytes, which is more than %d bytes window", frame.StreamID, len(frame.Data), window),
				code:  EWINDOWOVERFLOW,
			}
		}
		stream = newIncomingStream(c, frame.StreamID)
		if c.failure != nil {
			// we are closed and ignore valid DATA frames
			err := c.failure
			c.mu.Unlock()
			stream.CloseWithError(err)
			return nil
		}
		if c.shutdownchan != nil {
			// we are shutting down and should close incoming streams
			c.mu.Unlock()
			stream.CloseWithError(ECONNSHUTDOWN)
			return nil
		}
		c.finishgroup.Add(1)
		c.shutdowngroup.Add(1)
		go c.handleStream(c.handler, stream)
		c.mu.Unlock()
	} else {
		if stream == nil {
			return copperError{
				error: fmt.Errorf("stream 0x%08x cannot be found", frame.StreamID),
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
	if len(frame.Data) > 0 {
		if !c.outgoing.takeReadWindow(len(frame.Data)) {
			return copperError{
				error: fmt.Errorf("stream 0x%08x received %d bytes, which overflowed the connection window", stream.StreamID, len(frame.Data)),
				code:  EWINDOWOVERFLOW,
			}
		}
		c.outgoing.incrementReadWindow(len(frame.Data))
	}
	err := stream.processDataFrame(frame)
	if err != nil {
		return err
	}
	return nil
}

func (c *rawConn) processResetFrame(frame *ResetFrame) error {
	if frame.StreamID == 0 {
		// send ECONNCLOSED unless other error is pending
		c.outgoing.fail(ECONNSHUTDOWN)
		return frame.Error
	}
	stream := c.streams.find(frame.StreamID)
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

func (c *rawConn) processWindowFrame(frame *WindowFrame) error {
	if frame.StreamID == 0 {
		c.outgoing.changeWriteWindow(int(frame.Increment))
		return nil
	}
	stream := c.streams.find(frame.StreamID)
	if stream == nil {
		// it's ok to receive WINDOW for a dead stream
		return nil
	}
	return stream.processWindowFrame(frame)
}

func (c *rawConn) processSettingsFrame(frame *SettingsFrame) error {
	if frame.Flags.Has(FlagSettingsAck) {
		c.settings.handleAck()
		return nil
	}
	return c.settings.handleSettings(frame)
}

func (c *rawConn) processFrame(rawFrame Frame) bool {
	var err error
	switch frame := rawFrame.(type) {
	case *PingFrame:
		err = c.processPingFrame(frame)
	case *DataFrame:
		err = c.processDataFrame(frame)
	case *ResetFrame:
		err = c.processResetFrame(frame)
	case *WindowFrame:
		err = c.processWindowFrame(frame)
	case *SettingsFrame:
		err = c.processSettingsFrame(frame)
	default:
		err = EUNKNOWNFRAME
	}
	if err != nil {
		c.closeWithError(err)
		return false
	}
	return true
}

func (c *rawConn) readloop() {
	defer c.finishgroup.Done()
	for {
		rawFrame, err := c.reader.ReadFrame()
		if err != nil {
			c.closeWithError(err)
			break
		}
		if !c.processFrame(rawFrame) {
			break
		}
	}
	err := c.Err()
	c.pings.fail(err)
	c.mu.Lock()
	c.streams.failLocked(err)
	c.settings.failLocked(err)
	c.mu.Unlock()
}

func (c *rawConn) writeloop() {
	defer c.finishgroup.Done()
	defer c.conn.Close()

	lastwrite := c.lastwrite
	deadline := lastwrite.Add(c.settings.getRemoteInactivityTimeout() * 2 / 3)
	t := time.AfterFunc(deadline.Sub(time.Now()), func() {
		c.outgoing.wakeup()
	})
	defer t.Stop()
	for {
		ok := c.outgoing.writeFrames()
		newlastwrite := c.lastwrite
		if ok && lastwrite == newlastwrite && c.bw.Buffered() == 0 {
			// We haven't written anything for a while, write an empty frame
			err := c.writer.WriteData(0, 0, nil)
			if err != nil {
				c.closeWithError(err)
				break
			}
		}
		if c.bw.Buffered() > 0 {
			err := c.bw.Flush()
			if err != nil {
				c.closeWithError(err)
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
