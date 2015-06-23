package copper

import (
	"bufio"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const (
	inactivityTimeout = 60 * time.Second
	generatePingAfter = 2 * inactivityTimeout / 3
)

// Returned when stream handler is nil
var ErrInvalidStreamHandler = errors.New("stream handler cannot be nil")

// Returned when target id is not valid or not previously registered
var ErrInvalidTarget = errors.New("target must be greater than 0 and registered")

// Returned when connection is closed with a Close() call
var ErrConnClosed = errors.New("connection closed")

// StreamHandler is used to handle incoming streams
type StreamHandler interface {
	HandleStream(target int64, r io.Reader, w io.Writer)
}

// StreamHandlerFunc wraps a function to conform with StreamHandler interface
type StreamHandlerFunc func(target int64, r io.Reader, w io.Writer)

var _ StreamHandler = StreamHandlerFunc(nil)

// HandleStream calls the underlying function
func (f StreamHandlerFunc) HandleStream(target int64, r io.Reader, w io.Writer) {
	f(target, r, w)
}

// Conn is a multiplexed connection implementing the copper protocol
type Conn interface {
	Close() error
	OpenStream(target int64) (r io.Reader, w io.Writer, err error)
	AddHandler(handler StreamHandler) (target int64, err error)
	RemoveHandler(target int64) error
}

type rawConn struct {
	lock        sync.Mutex
	conn        net.Conn
	closed      bool
	failure     error
	signal      chan struct{}
	pingAcks    []uint64
	pingQueue   []uint64
	pingResults map[uint64][]chan error
	handlers    map[int64]StreamHandler
	nexthandler int64
}

var _ Conn = &rawConn{}

// NewConn wraps the underlying network connection with the copper protocol
func NewConn(conn net.Conn, handler StreamHandler) Conn {
	handlers := map[int64]StreamHandler{
		0: handler,
	}
	c := &rawConn{
		conn:        conn,
		closed:      false,
		signal:      make(chan struct{}, 1),
		pingResults: make(map[uint64][]chan error),
		handlers:    handlers,
		nexthandler: 1,
	}
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
		return nil
	}
	return c.failure
}

func (c *rawConn) closeWithError(err error) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.closeWithErrorLocked(err)
}

func (c *rawConn) Close() error {
	return c.closeWithError(ErrConnClosed)
}

func (c *rawConn) OpenStream(target int64) (io.Reader, io.Writer, error) {
	// TODO
	return nil, nil, nil
}

func (c *rawConn) AddHandler(handler StreamHandler) (int64, error) {
	if handler == nil {
		return 0, ErrInvalidStreamHandler
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	target := c.nexthandler
	c.nexthandler++
	c.handlers[target] = handler
	return target, nil
}

func (c *rawConn) RemoveHandler(target int64) error {
	if target <= 0 {
		return ErrInvalidTarget
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	if _, ok := c.handlers[target]; !ok {
		return ErrInvalidTarget
	}
	delete(c.handlers, target)
	return nil
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

func (c *rawConn) readloop() {
	r := bufio.NewReader(c.conn)
	for {
		c.conn.SetReadDeadline(time.Now().Add(inactivityTimeout))
		rawFrame, err := readFrame(r)
		if err != nil {
			c.closeWithError(err)
			break
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
	t := time.NewTimer(generatePingAfter)
	for {
		select {
		case <-c.signal:
		case <-t.C:
			datarequired = true
		}
		for {
			frames, err := c.prepareWriteBatch(datarequired)
			if err != nil {
				// Attempt to notify the other side that we have an error
				// It's ok if any of this fails, the read side will stop
				// when we close the connection.
				c.conn.SetWriteDeadline(time.Now().Add(inactivityTimeout))
				frame := fatalFrame{
					reason:  255, // TODO: implement reason codes
					message: []byte(err.Error()),
				}
				frame.writeFrameTo(w)
				w.Flush()
				c.conn.Close()
				return
			}
			datarequired = false
			if len(frames) == 0 {
				break
			}
			for _, frame := range frames {
				c.conn.SetWriteDeadline(time.Now().Add(inactivityTimeout))
				err = frame.writeFrameTo(w)
				if err != nil {
					c.closeWithError(err)
					c.conn.Close()
					return
				}
			}
		}
		if w.Buffered() > 0 {
			c.conn.SetWriteDeadline(time.Now().Add(inactivityTimeout))
			err := w.Flush()
			if err != nil {
				c.closeWithError(err)
				c.conn.Close()
				return
			}
		}
		t.Reset(generatePingAfter)
	}
}
