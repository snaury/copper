package copper

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const streamWriteBufferSize = 65536

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

	increment int // pending outgoing window increment

	ready condWithDeadline // signals when read() should be unblocked
}

func (r *rawStreamRead) init(l sync.Locker, window int) {
	r.left = window
	r.acked = make(chan struct{})
	r.closed = make(chan struct{})
	r.ready.init(l)
}

// write side of the stream
type rawStreamWrite struct {
	err   error  // non-nil if write side is closed (locally or remotely)
	buf   buffer // buffered outgoing bytes
	left  int    // number of bytes we are allowed to send
	wired int    // number of bytes in buf that are being written on the wire

	closed chan struct{} // closed when write side is closed

	ready   condWithDeadline // signals when write() should be unblocked
	flushed condWithDeadline // signals when flush() should be unblocked
}

func (w *rawStreamWrite) init(l sync.Locker, window int) {
	w.left = window
	w.ready.init(l)
	w.flushed.init(l)
	w.closed = make(chan struct{})
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

// Clears write buffer, called when remote closes its read side
// This function is called before setWriteErrorLocked for efficiency.
func (s *rawStream) clearWriteBufferLocked() {
	// After clearing the write buffer there's no need to broadcast to writes,
	// because closing write side will also broadcast, unless already closed.
	s.write.buf.clear()
	s.write.wired = 0
	s.write.flushed.Broadcast()
	if s.flags&flagStreamNeedEOF != 0 {
		// If we had data pending, then it's no longer the case (it will now
		// become a no-op), however we still need to send EOF, so switch over
		// to sending it using a ctrl.
		// Scheduling ctrl here implies that write side is already closed, so
		// setWriteErrorLocked will be a no-op (otherwise we'll schedule ctrl
		// when we close the write side)
		s.scheduleCtrl()
	}
}

func (s *rawStream) setReadDeadlineLocked(t time.Time) {
	s.read.ready.setDeadline(t)
}

func (s *rawStream) setWriteDeadlineLocked(t time.Time) {
	s.write.ready.setDeadline(t)
	s.write.flushed.setDeadline(t)
}

// Waits until a byte may be read, or read side is closed
func (s *rawStream) waitReadReadyLocked() error {
	for s.read.buf.size == 0 {
		if s.read.err != nil {
			return s.read.err
		}
		err := s.read.ready.Wait()
		if err != nil {
			return err
		}
	}
	return nil
}

// Waits until a byte may be written, or write side is closed
func (s *rawStream) waitWriteReadyLocked() error {
	for s.write.err == nil && s.write.buf.size >= streamWriteBufferSize {
		err := s.write.ready.Wait()
		if err != nil {
			return err
		}
	}
	return s.write.err
}

// Waits until all data has been flushed to the connection buffer, or write
// side is closed.
func (s *rawStream) waitFlushedLocked() error {
	for s.write.err == nil && s.write.buf.size > 0 {
		err := s.write.flushed.Wait()
		if err != nil {
			return err
		}
	}
	return s.write.err
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
			s.read.buf.write(data)
			s.read.ready.Broadcast()
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
		s.clearWriteBufferLocked()
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
	if s.write.left <= 0 && s.pendingBytes() > 0 {
		s.write.left += diff
		if s.write.left > 0 {
			s.scheduleData()
		}
	} else {
		s.write.left += diff
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

// Prepares a chunk of data for the wire, up to maxpayload bytes. The data is
// restricted by current debt and credited towards the connection window. For
// efficiency data is not removed from the write buffer, but is marked as
// wired, so flags calculation may correctly take it into account.
func (s *rawStream) prepareDataLocked(maxpayload int) []byte {
	n := s.write.buf.size
	if n > s.write.left {
		// We have more data in the buffer than stream window allows
		n = s.write.left
	}
	if n <= 0 {
		return nil
	}
	if n > maxpayload {
		n = maxpayload
	}
	n = s.conn.outgoing.takeWriteWindow(n)
	if n > 0 {
		data := s.write.buf.peek()[:n]
		s.write.wired = n
		s.write.left -= n
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
	if s.flags&flagStreamDiscard == flagStreamDiscard && s.write.buf.size == 0 {
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
			data := s.prepareDataLocked(MaxDataFramePayloadSize)
			flags := s.outgoingFlags()
			s.mu.Unlock()
			err := s.conn.writer.WriteData(s.streamID, flags, data)
			if err != nil {
				return err
			}
			s.mu.Lock()
			s.write.buf.discard(s.write.wired)
			s.write.wired = 0
			if s.write.buf.size < streamWriteBufferSize {
				s.write.ready.Broadcast()
			}
			if s.write.buf.size == 0 {
				s.write.flushed.Broadcast()
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
			if s.write.buf.size == 0 && s.flags&flagStreamNeedEOF != 0 {
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
		if s.write.buf.size == 0 && s.flags&flagStreamNeedEOF != 0 {
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
	data := s.prepareDataLocked(MaxDataFramePayloadSize)
	if len(data) > 0 {
		flags := s.outgoingFlags()
		if s.activeData() {
			// We have more data, make sure to re-register
			s.scheduleData()
		} else if s.activeReset() {
			// Sending all data unlocked a pending RESET
			s.scheduleCtrl()
		}
		s.mu.Unlock()
		err := s.conn.writer.WriteData(s.streamID, flags, data)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.write.buf.discard(s.write.wired)
		s.write.wired = 0
		if s.write.buf.size < streamWriteBufferSize {
			s.write.ready.Broadcast()
		}
		if s.write.buf.size == 0 {
			s.write.flushed.Broadcast()
		}
	}
	s.mu.Unlock()
	return nil
}

// Returns the number of pending bytes
func (s *rawStream) pendingBytes() int {
	return s.write.buf.size - s.write.wired
}

// Returns true if there is pending data that needs to be written
func (s *rawStream) activeData() bool {
	return s.write.left > 0 && s.pendingBytes() > 0
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
		if s.write.buf.size == s.write.wired && s.flags&flagStreamNeedEOF != 0 {
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
	if s.write.buf.size == s.write.wired && s.flags&flagStreamNeedEOF != 0 {
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
		s.read.increment = 0
		s.read.ready.Broadcast()
	}
}

// Sets the write error to err
func (s *rawStream) setWriteErrorLocked(err error) {
	if s.write.err == nil {
		s.write.err = err
		close(s.write.closed)
		s.flags |= flagStreamNeedEOF
		if s.write.buf.size == s.write.wired {
			// We had no pending data, but now we need to send a EOF, which can
			// only be done in a ctrl phase, so make sure to schedule it.
			s.scheduleCtrl()
		}
		s.write.ready.Broadcast()
		s.write.flushed.Broadcast()
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

func (s *rawStream) Peek() (b []byte, err error) {
	s.mu.Lock()
	err = s.waitReadReadyLocked()
	if err == nil {
		b = s.read.buf.peek()
		err = s.read.err
	}
	s.mu.Unlock()
	return
}

func (s *rawStream) Discard(n int) int {
	s.mu.Lock()
	n = s.read.buf.discard(n)
	if n > 0 {
		if s.read.err == nil {
			s.read.increment += n
			s.scheduleCtrl()
		}
	}
	s.mu.Unlock()
	return n
}

func (s *rawStream) Read(b []byte) (n int, err error) {
	s.mu.Lock()
	if len(b) > 0 {
		err = s.waitReadReadyLocked()
		if err != nil {
			s.mu.Unlock()
			return
		}
		n = s.read.buf.read(b)
		if s.read.err == nil {
			s.read.increment += n
			s.scheduleCtrl()
		}
	}
	if s.read.err != nil && s.read.buf.size == 0 {
		// there will be no more data, return the error too
		err = s.read.err
	}
	s.mu.Unlock()
	return
}

func (s *rawStream) ReadByte() (byte, error) {
	s.mu.Lock()
	err := s.waitReadReadyLocked()
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	b := s.read.buf.readbyte()
	if s.read.err == nil {
		s.read.increment++
		s.scheduleCtrl()
	}
	s.mu.Unlock()
	return b, nil
}

func (s *rawStream) Write(b []byte) (n int, err error) {
	s.mu.Lock()
	if len(b) == 0 {
		err = s.write.err
		s.mu.Unlock()
		return
	}
	for n < len(b) {
		err = s.waitWriteReadyLocked()
		if err != nil {
			break
		}
		taken := len(b) - n
		if taken > streamWriteBufferSize-s.write.buf.size {
			taken = streamWriteBufferSize - s.write.buf.size
		}
		s.write.buf.write(b[n : n+taken])
		if s.write.left > 0 {
			s.scheduleData()
		}
		n += taken
	}
	s.mu.Unlock()
	return
}

func (s *rawStream) WriteByte(b byte) error {
	s.mu.Lock()
	err := s.waitWriteReadyLocked()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.write.buf.writebyte(b)
	if s.write.left > 0 {
		s.scheduleData()
	}
	s.mu.Unlock()
	return nil
}

func (s *rawStream) Flush() error {
	s.mu.Lock()
	err := s.waitFlushedLocked()
	s.mu.Unlock()
	return err
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
