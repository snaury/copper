package copper

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	flagStreamSentEOF   = 0x01
	flagStreamSeenEOF   = 0x02
	flagStreamBothEOF   = flagStreamSentEOF | flagStreamSeenEOF
	flagStreamSentReset = 0x04
	flagStreamSeenAck   = 0x08
	flagStreamNeedOpen  = 0x10
	flagStreamNeedEOF   = 0x20
	flagStreamNeedReset = 0x40
	flagStreamNeedAck   = 0x80
	flagStreamDiscard   = flagStreamNeedOpen | flagStreamNeedEOF | flagStreamNeedReset
	flagStreamDataCtrl  = flagStreamNeedOpen | flagStreamNeedAck
)

// read side of the stream
type rawStreamRead struct {
	err  error  // non-nil if read side is closed (locally or remotely)
	buf  buffer // buffered incoming bytes
	left int    // number of bytes remote is allowed to send us

	acked  chan struct{} // closed when acknowledged or read side is closed
	closed chan struct{} // closed when read side is closed

	request []byte   // pending Read() request
	replies chan int // pending Read() results
	active  bool     // set to true while Read() is active

	increment int // pending outgoing window increment

	deadline deadlineChannel
}

func (r *rawStreamRead) init(l sync.Locker, window int) {
	r.left = window
	r.acked = make(chan struct{})
	r.closed = make(chan struct{})
	r.replies = make(chan int, 1)
}

// write side of the stream
type rawStreamWrite struct {
	err  error // non-nil if write side is closed (locally or remotely)
	left int   // number of bytes we are allowed to send

	closed chan struct{} // closed when write side is closed

	request []byte   // pending Write() request
	written int      // number of bytes written so far
	replies chan int // pending Write() results
	active  bool     // set to true while Write() is active

	deadline deadlineChannel
}

func (w *rawStreamWrite) init(l sync.Locker, window int) {
	w.left = window
	w.closed = make(chan struct{})
	w.replies = make(chan int, 1)
}

// rawStream tracks state of the copper stream
type rawStream struct {
	conn     *rawConn
	streamID uint32

	mu    sync.RWMutex
	flags int            // stream state flags
	read  rawStreamRead  // read side of the stream
	write rawStreamWrite // write side of the stream
	reset error          // error used to close the stream

	closed chan struct{} // closed when close is called

	// these are protected by the outgoing lock
	inctrl bool // stream is in ctrl queue
	indata bool // stream is in data queue
}

var _ Stream = &rawStream{}

// Creates and registers a new stream, conn.mu must be locked
func newStream(conn *rawConn, streamID uint32) *rawStream {
	s := &rawStream{
		conn:     conn,
		streamID: streamID,
		closed:   make(chan struct{}),
	}
	s.read.init(&s.mu, conn.settings.localStreamWindowSize)
	s.write.init(&s.mu, conn.settings.remoteStreamWindowSize)
	conn.streams.addLocked(s)
	return s
}

// Creates and registers a new incoming stream, conn.mu must be locked
func newIncomingStream(conn *rawConn, streamID uint32) *rawStream {
	return newStream(conn, streamID)
}

// Creates and registers a new outgoing stream, conn.mu must be locked
func newOutgoingStream(conn *rawConn, streamID uint32) *rawStream {
	s := newStream(conn, streamID)
	s.flags |= flagStreamNeedOpen
	s.scheduleCtrl()
	return s
}

// Adds the stream to the ctrl queue
func (s *rawStream) scheduleCtrl() {
	s.conn.outgoing.addCtrl(s)
}

// Adds the stream to the data queue
func (s *rawStream) scheduleData() {
	s.conn.outgoing.addData(s)
}

// Remove the stream from live streams if both sides are closed
func (s *rawStream) cleanupLocked() {
	if s.flags&flagStreamBothEOF == flagStreamBothEOF {
		s.conn.streams.remove(s)
	}
}

func (s *rawStream) setReadDeadlineLocked(t time.Time) {
	s.read.deadline.setDeadline(t)
}

func (s *rawStream) setWriteDeadlineLocked(t time.Time) {
	s.write.deadline.setDeadline(t)
}

// Processes incoming data, returns a connection-level error on failure
func (s *rawStream) processDataLocked(data []byte, flags FrameFlags) error {
	if s.flags&flagStreamSeenEOF != 0 {
		return copperError{
			error: fmt.Errorf("stream 0x%08x cannot have DATA after EOF", s.streamID),
			code:  EINVALIDSTREAM,
		}
	}
	if flags.Has(FlagDataAck) {
		if s.read.err == nil && s.flags&flagStreamSeenAck == 0 {
			s.flags |= flagStreamSeenAck
			close(s.read.acked)
		} else {
			s.flags |= flagStreamSeenAck
		}
	}
	if len(data) > 0 {
		if len(data) > s.read.left {
			return copperError{
				error: fmt.Errorf("stream 0x%08x received %d bytes, which is more than %d bytes window", s.streamID, len(data), s.read.left),
				code:  EWINDOWOVERFLOW,
			}
		}
		if s.read.err == nil {
			if s.read.active {
				n := copy(s.read.request, data)
				s.read.replies <- n
				s.read.active = false
				s.read.buf.write(data[n:])
			} else {
				s.read.buf.write(data)
			}
		}
		s.read.left -= len(data)
	}
	if flags.Has(FlagDataEOF) {
		s.flags |= flagStreamSeenEOF
		s.setReadErrorLocked(io.EOF)
		s.cleanupLocked()
	}
	return nil
}

// Processes an incoming DATA frame, returns connection-level error on failure
func (s *rawStream) processDataFrame(frame *DataFrame) error {
	s.mu.Lock()
	err := s.processDataLocked(frame.Data, frame.Flags)
	s.mu.Unlock()
	return err
}

// Processes an incoming RESET frame, returns connection-level error on failure
func (s *rawStream) processResetFrame(frame *ResetFrame) error {
	s.mu.Lock()
	if frame.Flags.Has(FlagResetRead) {
		err := frame.Error
		s.setWriteErrorLocked(err)
	}
	if frame.Flags.Has(FlagResetWrite) {
		err := frame.Error
		if err == ECLOSED {
			// ECLOSED is special, it translates to a normal EOF
			err = io.EOF
		}
		s.flags |= flagStreamSeenEOF
		s.setReadErrorLocked(err)
		s.cleanupLocked()
	}
	s.mu.Unlock()
	return nil
}

// Changes the size of the write window (WINDOW or SETTINGS frames)
func (s *rawStream) changeWriteWindow(diff int) {
	s.mu.Lock()
	if s.write.err == nil {
		if s.write.active && s.write.left <= 0 && len(s.write.request) > 0 {
			s.write.left += diff
			if s.write.left > 0 {
				// If we gained enough window we need to reschedule
				s.scheduleData()
			}
		} else {
			s.write.left += diff
		}
	}
	s.mu.Unlock()
}

// Processes an incoming WINDOW frame
func (s *rawStream) processWindowFrame(frame *WindowFrame) error {
	if frame.Increment <= 0 {
		return copperError{
			error: fmt.Errorf("stream 0x%08x received invalid increment %d", s.streamID, frame.Increment),
			code:  EINVALIDFRAME,
		}
	}
	s.changeWriteWindow(int(frame.Increment))
	return nil
}

// Flushes the connection write buffer if a DATA frame with at least mindata
// bytes cannot be written without blocking. Returns the maximum number of
// bytes that may fit in a DATA frame without blocking or an error. When
// error is not nil the lock is not re-taken!
func (s *rawStream) flushBeforeDataLocked(mindata int) (maxdata int, flushed bool, err error) {
	maxdata = s.conn.bw.Available() - FrameHeaderSize
	if maxdata < mindata {
		s.mu.Unlock()
		flushed = true
		err = s.conn.bw.Flush()
		if err != nil {
			return
		}
		maxdata = s.conn.bw.Available() - FrameHeaderSize
		if maxdata < mindata {
			panic(fmt.Errorf("flushing freed %d bytes DATA frame, when %d is the minimum", maxdata, mindata))
		}
		flushed = true
		s.mu.Lock()
	}
	return
}

// Prepares a chunk of data for the wire, up to maxpayload bytes. The data is
// taken directly from request and charged against both stream and connection
// windows. It is important to not unlock until this data is properly added
// to the request reply.
func (s *rawStream) prepareDataLocked(maxpayload int) []byte {
	if s.write.left <= 0 || !s.write.active || len(s.write.request) == 0 {
		// There is no pending write request, or no available window
		return nil
	}
	n := len(s.write.request)
	if n > maxpayload {
		n = maxpayload
	}
	if n > s.write.left {
		n = s.write.left
	}
	if n > MaxDataFramePayloadSize {
		n = MaxDataFramePayloadSize
	}
	n = s.conn.outgoing.takeWriteWindow(n)
	if n > 0 {
		data := s.write.request[:n]
		s.write.left -= n
		// Need to shrink the request, but it must not become nil
		s.write.request = s.write.request[n:]
		return data
	}
	return nil
}

// This function is called when stream is allowed to send control frames, but
// stream may send data as well if there is enough window. Before this function
// is called the stream is removed from the ctrl queue and must be re-added if
// this function needs to be called again. Currently it writes as many frames
// as it can, but a different strategy may be to write a single frame and
// re-register.
func (s *rawStream) writeCtrl() error {
	s.mu.Lock()
	if s.flags&flagStreamDiscard == flagStreamDiscard && s.write.request == nil {
		// If stream was opened, and then immediately closed, then we don't
		// have to send any frames at all, just pretend it all happened
		// already and move on.
		s.flags = flagStreamSentEOF | flagStreamSeenEOF | flagStreamSentReset
		s.cleanupLocked()
		s.mu.Unlock()
		return nil
	}
	// Keep going as long as this stream needs to send frames
	for {
		if s.flags&flagStreamDataCtrl != 0 && s.flags&flagStreamSentEOF == 0 {
			// Write DATA frame if we need to send OPEN or ACK
			maxdata, _, err := s.flushBeforeDataLocked(0)
			if err != nil {
				// The stream is already unlocked
				return err
			}
			data := s.prepareDataLocked(maxdata)
			flags := s.outgoingFlags()
			err = s.conn.writer.WriteData(s.streamID, flags, data)
			if err != nil {
				s.mu.Unlock()
				return err
			}
			s.write.written += len(data)
			if len(s.write.request) == 0 && s.write.active {
				// We have written all data from the request
				s.write.replies <- s.write.written
				s.write.active = false
			}
			continue
		}
		if s.read.increment > 0 {
			// Write WINDOW frame if we want the remote to send us more data
			increment := uint32(s.read.increment)
			s.read.left += s.read.increment
			s.read.increment = 0
			s.mu.Unlock()
			err := s.conn.writer.WriteWindow(s.streamID, increment)
			if err != nil {
				return err
			}
			s.mu.Lock()
			continue
		}
		if s.activeReset() {
			reset := s.reset
			if reset == nil {
				// The only way we may end up with reset being nil, but with an
				// outgoing RESET pending, is when we haven't seen a EOF yet, but
				// either CloseRead() or CloseReadError() was called. In both of
				// those cases the error is in read.
				reset = s.read.err
			}
			if reset == ECONNCLOSED {
				// Instead of ECONNCLOSED remote should receive ECONNSHUTDOWN
				reset = ECONNSHUTDOWN
			}
			var flags FrameFlags
			if s.flags&(flagStreamSeenEOF|flagStreamSentReset) == 0 {
				flags |= FlagResetRead
			}
			if s.pendingData() == 0 && s.flags&flagStreamNeedEOF != 0 {
				// This RESET closes both sides of the stream, otherwise it only
				// closes the read side and write side is delayed until we need to
				// send EOF.
				s.flags &^= flagStreamNeedReset
				s.flags &^= flagStreamNeedEOF
				s.flags |= flagStreamSentEOF
				s.cleanupLocked()
				flags |= FlagResetWrite
			}
			// N.B.: we may send RESET twice. First without EOF, to stop the other
			// side from sending us more data. Second with EOF, after sending all
			// our pending data, to convey the error message to the other side.
			s.flags |= flagStreamSentReset
			s.mu.Unlock()
			err := s.conn.writer.WriteReset(s.streamID, flags, reset)
			if err != nil {
				return err
			}
			s.mu.Lock()
			continue
		}
		if s.pendingData() == 0 && s.flags&flagStreamNeedEOF != 0 {
			// We have an empty write buffer with a pending EOF. Since writeData
			// callback is only called when there is pending data and sending
			// EOF doesn't cost us any window bytes we do it right here.
			flags := s.outgoingFlags()
			s.mu.Unlock()
			err := s.conn.writer.WriteData(s.streamID, flags, nil)
			if err != nil {
				return err
			}
			s.mu.Lock()
			continue
		}
		// We've sent everything we could
		break
	}
	s.mu.Unlock()
	return nil
}

// This function is called when stream is allowed to send data frames. On entry
// there is at least one byte in the connection window, but there may be nothing
// left to write if it was consumed elsewhere. If there is no data to write it
// doesn't do anything, since writing an empty EOF frame is done by writeCtrl.
func (s *rawStream) writeData() error {
	s.mu.Lock()
	for s.activeData() {
		maxdata, flushed, err := s.flushBeforeDataLocked(1)
		if err != nil {
			// The stream is already unlocked
			return err
		}
		if flushed {
			// Make sure request is still active
			continue
		}
		data := s.prepareDataLocked(maxdata)
		if len(data) == 0 {
			// Cannot write data for some reason
			s.scheduleData()
			s.mu.Unlock()
			return nil
		}
		flags := s.outgoingFlags()
		err = s.conn.writer.WriteData(s.streamID, flags, data)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		s.write.written += len(data)
		if len(s.write.request) == 0 && s.write.active {
			// We have written all the data from the request
			s.write.replies <- s.write.written
			s.write.active = false
			if s.activeReset() {
				// Sending all data unlocked a pending RESET
				s.scheduleCtrl()
			}
			s.mu.Unlock()
			return nil
		}
	}
	s.mu.Unlock()
	return nil
}

// Returns the length of pending write request
func (s *rawStream) pendingData() int {
	if s.write.active {
		return len(s.write.request)
	}
	return 0
}

// Returns true if there is pending data that needs to be written
func (s *rawStream) activeData() bool {
	return s.write.active && s.write.left > 0 && len(s.write.request) > 0
}

// Returns true if there is a RESET frame pending and it needs to be written
func (s *rawStream) activeReset() bool {
	if s.flags&flagStreamNeedReset != 0 {
		// there's a RESET frame pending
		if s.flags&flagStreamBothEOF == flagStreamBothEOF {
			// both sides already closed, don't need to send anything
			s.flags &^= flagStreamNeedReset
			return false
		}
		if s.flags&(flagStreamSeenEOF|flagStreamSentReset) == 0 {
			// haven't seen EOF yet, so send RESET as soon as possible
			return true
		}
		if s.reset == nil || s.reset == ECLOSED {
			// without an error it's better to send DATA with EOF flag set
			s.flags &^= flagStreamNeedReset
			return false
		}
		if s.pendingData() == 0 && s.flags&flagStreamNeedEOF != 0 {
			// need to send EOF now and close the write side
			return true
		}
		// must delay RESET until we need to send EOF
		return false
	}
	return false
}

// Returns true if there is EOF pending and it needs to be written now
func (s *rawStream) activeEOF() bool {
	if s.pendingData() == 0 && s.flags&flagStreamNeedEOF != 0 {
		// there's no data and a EOF is pending
		if s.flags&flagStreamNeedReset != 0 {
			// send EOF only when pending RESET is without an error
			return s.reset == nil || s.reset == ECLOSED
		}
		return true
	}
	return false
}

// Returns outgoing flags that should be used for DATA frames
// This function must be called exactly once per frame, since it clears various
// flags before returning and will not return the same flags a second time.
func (s *rawStream) outgoingFlags() FrameFlags {
	var flags FrameFlags
	if s.flags&flagStreamNeedOpen != 0 {
		s.flags &^= flagStreamNeedOpen
		flags |= FlagDataOpen
	}
	if s.flags&flagStreamNeedAck != 0 {
		s.flags &^= flagStreamNeedAck
		flags |= FlagDataAck
	}
	if s.activeEOF() {
		s.flags &^= flagStreamNeedEOF
		s.flags |= flagStreamSentEOF
		s.cleanupLocked()
		flags |= FlagDataEOF
	}
	return flags
}

// Schedules a RESET on the read side, preventing remote from sending more data
func (s *rawStream) resetReadSideLocked() {
	if s.flags&flagStreamSeenEOF == 0 {
		s.flags |= flagStreamNeedReset
		if s.activeReset() {
			s.scheduleCtrl()
		}
	}
}

// Schedules a RESET on both sides, so not only the remote is prevented from
// sending more data, it also receives an error code in any of its read calls.
func (s *rawStream) resetBothSidesLocked() {
	if s.flags&flagStreamBothEOF != flagStreamBothEOF {
		s.flags |= flagStreamNeedReset
		if s.activeReset() {
			s.scheduleCtrl()
		}
	}
}

// Sets the read error to err
func (s *rawStream) setReadErrorLocked(err error) {
	if s.read.err == nil {
		s.read.err = err
		close(s.read.closed)
		if s.flags&flagStreamSeenAck == 0 {
			close(s.read.acked)
		}
		if s.read.active {
			s.read.replies <- 0
			s.read.active = false
		}
		s.read.increment = 0
	}
}

// Sets the write error to err
func (s *rawStream) setWriteErrorLocked(err error) {
	if s.write.err == nil {
		s.write.err = err
		close(s.write.closed)
		s.write.left = 0
		s.flags |= flagStreamNeedEOF
		if s.write.active {
			s.write.replies <- s.write.written
			s.write.active = false
		}
		// Need to send EOF, which can only be done in writeCtrl in the absense
		// of pending data, make sure to schedule that.
		s.scheduleCtrl()
	}
}

func (s *rawStream) closeWithErrorLocked(err error) error {
	if err == nil || err == io.EOF {
		err = ECLOSED
	}
	preverror := s.reset
	if preverror == nil {
		s.reset = err
		close(s.closed)
		s.setReadErrorLocked(err)
		s.setWriteErrorLocked(err)
		s.resetBothSidesLocked()
	}
	return preverror
}

// emptyReadBuffer is used by Peek() to make an empty read request
var emptyReadBuffer = []byte{}

func (s *rawStream) Peek() (b []byte, err error) {
	s.mu.Lock()
	if s.read.request != nil {
		s.mu.Unlock()
		return nil, errMultipleReads
	}
	if s.read.buf.size == 0 && s.read.err == nil {
		// Read buffer is empty, schedule a request
		s.read.request = emptyReadBuffer
		s.read.active = true
		timeout := s.read.deadline.expired
		s.mu.Unlock()
		select {
		case <-s.read.replies:
			s.mu.Lock()
		case <-timeout:
			s.mu.Lock()
			if s.read.active {
				// timeout, cancel the request
				s.read.active = false
				err = errTimeout
			} else {
				// timeout and reply were simultaneous
				<-s.read.replies
			}
		}
		s.read.request = nil
	}
	if s.read.buf.size > 0 {
		b = s.read.buf.peek()
	}
	if s.read.err != nil {
		err = s.read.err
	}
	s.mu.Unlock()
	return
}

func (s *rawStream) Discard(n int) int {
	s.mu.Lock()
	n = s.read.buf.discard(n)
	if n > 0 && s.read.err == nil {
		s.read.increment += n
		s.scheduleCtrl()
	}
	s.mu.Unlock()
	return n
}

func (s *rawStream) Read(b []byte) (n int, err error) {
	s.mu.Lock()
	if s.read.request != nil {
		s.mu.Unlock()
		return 0, errMultipleReads
	}
	if len(b) > 0 && s.read.buf.size > 0 {
		// Take some data from the buffer
		n = s.read.buf.read(b)
	} else if len(b) > 0 && s.read.err == nil {
		// Read buffer is empty, schedule a request
		s.read.request = b
		s.read.active = true
		timeout := s.read.deadline.expired
		s.mu.Unlock()
		select {
		case n = <-s.read.replies:
			s.mu.Lock()
		case <-timeout:
			s.mu.Lock()
			if s.read.active {
				// timeout, cancel the request
				s.read.active = false
				err = errTimeout
			} else {
				// timeout and reply were simultaneous
				n = <-s.read.replies
			}
		}
		s.read.request = nil
	}
	if n > 0 && s.read.err == nil {
		s.read.increment += n
		s.scheduleCtrl()
	}
	if s.read.err != nil && s.read.buf.size == 0 {
		// there will be no more data, return the error too
		err = s.read.err
	}
	s.mu.Unlock()
	return
}

func (s *rawStream) Write(b []byte) (n int, err error) {
	s.mu.Lock()
	if s.write.request != nil {
		s.mu.Unlock()
		return 0, errMultipleWrites
	}
	if len(b) > 0 && s.write.err == nil {
		s.write.request = b
		s.write.written = 0
		s.write.active = true
		if s.activeData() {
			s.scheduleData()
		}
		timeout := s.write.deadline.expired
		s.mu.Unlock()
		select {
		case n = <-s.write.replies:
			s.mu.Lock()
		case <-timeout:
			s.mu.Lock()
			if s.write.active {
				// timeout, cancel the request
				s.write.active = false
				n = s.write.written
				err = errTimeout
			} else {
				// timeout and reply were simultaneous
				n = <-s.write.replies
			}
		}
		s.write.request = nil
		if n < len(b) && s.write.err != nil {
			err = s.write.err
		}
	} else {
		err = s.write.err
	}
	s.mu.Unlock()
	return
}

func (s *rawStream) Closed() <-chan struct{} {
	return s.closed
}

func (s *rawStream) ReadErr() error {
	s.mu.RLock()
	err := s.read.err
	s.mu.RUnlock()
	return err
}

func (s *rawStream) ReadClosed() <-chan struct{} {
	return s.read.closed
}

func (s *rawStream) WriteErr() error {
	s.mu.RLock()
	err := s.write.err
	s.mu.RUnlock()
	return err
}

func (s *rawStream) WriteClosed() <-chan struct{} {
	return s.write.closed
}

func (s *rawStream) Acknowledge() error {
	s.mu.Lock()
	err := s.write.err
	if err == nil {
		s.flags |= flagStreamNeedAck
		s.scheduleCtrl()
	}
	s.mu.Unlock()
	return err
}

func (s *rawStream) Acknowledged() <-chan struct{} {
	return s.read.acked
}

func (s *rawStream) IsAcknowledged() bool {
	s.mu.RLock()
	acked := s.flags&flagStreamSeenAck != 0
	s.mu.RUnlock()
	return acked
}

func (s *rawStream) Close() error {
	s.mu.Lock()
	err := s.closeWithErrorLocked(nil)
	s.mu.Unlock()
	return err
}

func (s *rawStream) CloseRead() error {
	s.mu.Lock()
	preverror := s.read.err
	s.setReadErrorLocked(ECLOSED)
	s.resetReadSideLocked()
	s.mu.Unlock()
	return preverror
}

func (s *rawStream) CloseReadError(err error) error {
	if err == nil || err == io.EOF {
		err = ECLOSED
	}
	s.mu.Lock()
	preverror := s.read.err
	s.setReadErrorLocked(err)
	s.resetReadSideLocked()
	s.mu.Unlock()
	return preverror
}

func (s *rawStream) CloseWrite() error {
	s.mu.Lock()
	preverror := s.write.err
	s.setWriteErrorLocked(ECLOSED)
	s.mu.Unlock()
	return preverror
}

func (s *rawStream) CloseWithError(err error) error {
	s.mu.Lock()
	preverror := s.closeWithErrorLocked(err)
	s.mu.Unlock()
	return preverror
}

func (s *rawStream) SetDeadline(t time.Time) error {
	s.mu.Lock()
	s.setReadDeadlineLocked(t)
	s.setWriteDeadlineLocked(t)
	s.mu.Unlock()
	return nil
}

func (s *rawStream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	s.setReadDeadlineLocked(t)
	s.mu.Unlock()
	return nil
}

func (s *rawStream) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	s.setWriteDeadlineLocked(t)
	s.mu.Unlock()
	return nil
}

func (s *rawStream) StreamID() uint32 {
	return s.streamID
}

func (s *rawStream) LocalAddr() net.Addr {
	return s.conn.conn.LocalAddr()
}

func (s *rawStream) RemoteAddr() net.Addr {
	return s.conn.conn.RemoteAddr()
}
