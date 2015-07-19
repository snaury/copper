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

type rawStreamRead struct {
	err    error
	buf    buffer
	left   int
	ready  condWithDeadline
	acked  chan struct{}
	closed chan struct{}

	increment int
}

func (r *rawStreamRead) init(l sync.Locker, window int) {
	r.left = window
	r.ready.init(l)
	r.acked = make(chan struct{})
	r.closed = make(chan struct{})
}

type rawStreamWrite struct {
	err     error
	buf     buffer
	left    int
	wired   int
	ready   condWithDeadline
	flushed condWithDeadline
	closed  chan struct{}
}

func (w *rawStreamWrite) init(l sync.Locker, window int) {
	w.left = window
	w.ready.init(l)
	w.flushed.init(l)
	w.closed = make(chan struct{})
}

type rawStream struct {
	mu       sync.RWMutex
	owner    *rawConn
	streamID uint32
	outgoing bool
	flags    int
	read     rawStreamRead
	write    rawStreamWrite
	reset    error

	// these are protected by the outgoing lock
	inctrl bool
	indata bool
}

var _ Stream = &rawStream{}

func newStream(owner *rawConn, streamID uint32) *rawStream {
	s := &rawStream{
		owner:    owner,
		streamID: streamID,
	}
	s.read.init(&s.mu, owner.settings.localStreamWindowSize)
	s.write.init(&s.mu, owner.settings.remoteStreamWindowSize)
	return s
}

func newIncomingStreamWithUnlock(owner *rawConn, streamID uint32) *rawStream {
	s := newStream(owner, streamID)
	owner.streams.addLockedWithUnlock(s)
	return s
}

func newOutgoingStreamWithUnlock(owner *rawConn, streamID uint32) *rawStream {
	s := newStream(owner, streamID)
	s.outgoing = true
	s.flags |= flagStreamNeedOpen
	owner.streams.addLockedWithUnlock(s)
	s.scheduleCtrl()
	return s
}

func (s *rawStream) canReceive() bool {
	s.mu.RLock()
	ok := s.read.err == nil
	s.mu.RUnlock()
	return ok
}

func (s *rawStream) scheduleCtrl() {
	s.owner.outgoing.addCtrl(s)
}

func (s *rawStream) scheduleData() {
	s.owner.outgoing.addData(s)
}

func (s *rawStream) cleanupLocked() {
	if s.flags&flagStreamBothEOF == flagStreamBothEOF {
		s.owner.streams.remove(s)
	}
}

func (s *rawStream) clearReadBufferLocked() {
	if s.read.buf.size > 0 {
		s.read.buf.clear()
	}
}

func (s *rawStream) clearWriteBufferLocked() {
	s.write.buf.clear()
	s.write.wired = 0
	s.write.flushed.Broadcast()
	if s.flags&flagStreamNeedEOF != 0 {
		// If we had data pending, then it's no longer the case (it will now
		// become a no-op), however we still need to send EOF, so switch over
		// to sending it using a ctrl.
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

func (s *rawStream) waitWriteReadyLocked() error {
	for s.write.err == nil && s.write.left <= 0 {
		err := s.write.ready.Wait()
		if err != nil {
			return err
		}
	}
	return s.write.err
}

func (s *rawStream) waitFlushLocked() error {
	for s.write.buf.size > 0 {
		if s.reset != nil {
			break
		}
		err := s.write.flushed.Wait()
		if err != nil {
			return err
		}
	}
	return s.write.err
}

func (s *rawStream) processDataLocked(data []byte, flags uint8) error {
	if s.flags&flagStreamSeenEOF != 0 {
		return copperError{
			error: fmt.Errorf("stream 0x%08x cannot have DATA after EOF", s.streamID),
			code:  EINVALIDSTREAM,
		}
	}
	if flags&flagDataAck != 0 {
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
				error: fmt.Errorf("stream 0x%08x received %d+%d bytes, which is more than %d bytes window", s.streamID, s.read.buf.size, len(data), s.read.left),
				code:  EWINDOWOVERFLOW,
			}
		}
		if s.read.err == nil {
			s.read.buf.write(data)
			s.read.ready.Broadcast()
		}
		s.read.left -= len(data)
	}
	if flags&flagDataEOF != 0 {
		s.flags |= flagStreamSeenEOF
		s.setReadErrorLocked(io.EOF)
		s.cleanupLocked()
	}
	return nil
}

func (s *rawStream) processDataFrame(frame *dataFrame) error {
	s.mu.Lock()
	err := s.processDataLocked(frame.data, frame.flags)
	s.mu.Unlock()
	return err
}

func (s *rawStream) processResetFrame(frame *resetFrame) error {
	s.mu.Lock()
	if frame.flags&flagResetRead != 0 {
		err := frame.err
		s.clearWriteBufferLocked()
		s.setWriteErrorLocked(err)
	}
	if frame.flags&flagResetWrite != 0 {
		err := frame.err
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

func (s *rawStream) changeWindow(diff int) {
	s.mu.Lock()
	if s.write.err == nil {
		wasactive := s.activeData()
		s.write.left += diff
		if !wasactive && s.activeData() {
			s.scheduleData()
		}
		if s.write.left > 0 {
			s.write.ready.Broadcast()
		}
	}
	s.mu.Unlock()
}

func (s *rawStream) processWindowFrame(frame *windowFrame) error {
	if frame.increment <= 0 {
		return copperError{
			error: fmt.Errorf("stream 0x%08x received invalid increment %d", s.streamID, frame.increment),
			code:  EINVALIDFRAME,
		}
	}
	s.changeWindow(int(frame.increment))
	return nil
}

func (s *rawStream) prepareDataLocked(maxpayload int) []byte {
	n := s.write.buf.size
	if s.write.left < 0 {
		// Incoming SETTINGS reduced our window and we have extra data in our
		// buffer, however we cannot send that extra data until there's a
		// window on the remote side.
		n += s.write.left
	}
	if n > maxpayload {
		n = maxpayload
	}
	n = s.owner.outgoing.takeSpace(n)
	if n > 0 {
		data := s.write.buf.peek()
		if len(data) > n {
			data = data[:n]
		}
		s.write.wired = n
		return data
	}
	return nil
}

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
	// keep going as long as this stream needs to send control frames
	for {
		if s.flags&flagStreamDataCtrl != 0 && s.flags&flagStreamSentEOF == 0 {
			data := s.prepareDataLocked(maxDataFramePayloadSize)
			frame := &dataFrame{
				streamID: s.streamID,
				flags:    s.outgoingFlags(),
				data:     data,
			}
			s.mu.Unlock()
			err := s.owner.writeFrame(frame)
			if err != nil {
				return err
			}
			s.mu.Lock()
			s.write.buf.discard(s.write.wired)
			s.write.wired = 0
			if s.write.buf.size == 0 {
				s.write.flushed.Broadcast()
			}
			continue
		}
		if s.read.increment > 0 {
			frame := &windowFrame{
				streamID:  s.streamID,
				increment: uint32(s.read.increment),
			}
			s.read.increment = 0
			s.mu.Unlock()
			err := s.owner.writeFrame(frame)
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
			flags := uint8(0)
			if s.flags&(flagStreamSeenEOF|flagStreamSentReset) == 0 {
				flags |= flagResetRead
			}
			if s.write.buf.size == 0 && s.flags&flagStreamNeedEOF != 0 {
				// This RESET closes both sides of the stream, otherwise it only
				// closes the read side and write side is delayed until we need to
				// send EOF.
				s.flags &^= flagStreamNeedReset
				s.flags &^= flagStreamNeedEOF
				s.flags |= flagStreamSentEOF
				s.cleanupLocked()
				flags |= flagResetWrite
			}
			// N.B.: we may send RESET twice. First without EOF, to stop the other
			// side from sending us more data. Second with EOF, after sending all
			// our pending data, to convey the error message to the other side.
			s.flags |= flagStreamSentReset
			frame := &resetFrame{
				streamID: s.streamID,
				flags:    flags,
				err:      reset,
			}
			s.mu.Unlock()
			err := s.owner.writeFrame(frame)
			if err != nil {
				return err
			}
			s.mu.Lock()
			continue
		}
		if s.write.buf.size == 0 && s.flags&flagStreamNeedEOF != 0 {
			frame := &dataFrame{
				streamID: s.streamID,
				flags:    s.outgoingFlags(),
			}
			s.mu.Unlock()
			err := s.owner.writeFrame(frame)
			if err != nil {
				return err
			}
			s.mu.Lock()
			continue
		}
		// we've sent everything we could
		break
	}
	s.mu.Unlock()
	return nil
}

func (s *rawStream) writeData() error {
	s.mu.Lock()
	data := s.prepareDataLocked(maxDataFramePayloadSize)
	if len(data) > 0 {
		frame := &dataFrame{
			streamID: s.streamID,
			flags:    s.outgoingFlags(),
			data:     data,
		}
		if s.activeData() {
			// we have more data, make sure to re-register
			s.scheduleData()
		} else if s.activeReset() {
			// sending all data unlocked a pending RESET
			s.scheduleCtrl()
		}
		s.mu.Unlock()
		err := s.owner.writeFrame(frame)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.write.buf.discard(s.write.wired)
		s.write.wired = 0
		if s.write.buf.size == 0 {
			s.write.flushed.Broadcast()
		}
	}
	s.mu.Unlock()
	return nil
}

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

func (s *rawStream) outgoingFlags() uint8 {
	var flags uint8
	if s.flags&flagStreamNeedOpen != 0 {
		s.flags &^= flagStreamNeedOpen
		flags |= flagDataOpen
	}
	if s.flags&flagStreamNeedAck != 0 {
		s.flags &^= flagStreamNeedAck
		flags |= flagDataAck
	}
	if s.activeEOF() {
		s.flags &^= flagStreamNeedEOF
		s.flags |= flagStreamSentEOF
		s.cleanupLocked()
		flags |= flagDataEOF
	}
	return flags
}

func (s *rawStream) activeData() bool {
	pending := s.write.buf.size - s.write.wired
	if s.write.left >= 0 {
		return pending > 0
	}
	return pending+s.write.left > 0
}

func (s *rawStream) resetReadSideLocked() {
	if s.flags&flagStreamSeenEOF == 0 {
		s.flags |= flagStreamNeedReset
		if s.activeReset() {
			s.scheduleCtrl()
		}
	}
}

func (s *rawStream) resetBothSidesLocked() {
	if s.flags&flagStreamBothEOF != flagStreamBothEOF {
		s.flags |= flagStreamNeedReset
		if s.activeReset() {
			s.scheduleCtrl()
		}
	}
}

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

func (s *rawStream) setWriteErrorLocked(err error) {
	if s.write.err == nil {
		s.write.err = err
		close(s.write.closed)
		s.write.left = 0
		s.flags |= flagStreamNeedEOF
		if s.write.buf.size == s.write.wired {
			// We had no pending data, but now we need to send a EOF, which can
			// only be done in a ctrl phase, so make sure to schedule it.
			s.scheduleCtrl()
		}
		s.write.ready.Broadcast()
	}
}

func (s *rawStream) closeWithError(err error, closed bool) error {
	s.mu.Lock()
	preverror := s.closeWithErrorLocked(err, closed)
	s.mu.Unlock()
	return preverror
}

func (s *rawStream) closeWithErrorLocked(err error, closed bool) error {
	if err == nil || err == io.EOF {
		err = ECLOSED
	}
	preverror := s.reset
	if preverror == nil {
		s.reset = err
		s.setReadErrorLocked(err)
		s.setWriteErrorLocked(err)
		s.write.flushed.Broadcast()
	}
	if closed {
		s.clearReadBufferLocked()
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
		s.read.left += n
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
		s.read.left += n
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
	s.read.left++
	if s.read.err == nil {
		s.read.increment++
		s.scheduleCtrl()
	}
	s.mu.Unlock()
	return b, nil
}

func (s *rawStream) Write(b []byte) (n int, err error) {
	s.mu.Lock()
	for n < len(b) {
		err = s.waitWriteReadyLocked()
		if err != nil {
			break
		}
		taken := len(b) - n
		if taken > s.write.left {
			taken = s.write.left
		}
		s.write.buf.write(b[n : n+taken])
		s.write.left -= taken
		s.scheduleData()
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
	s.write.left--
	s.scheduleData()
	s.mu.Unlock()
	return nil
}

func (s *rawStream) Flush() error {
	s.mu.Lock()
	err := s.waitFlushLocked()
	s.mu.Unlock()
	return err
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
	err := s.closeWithErrorLocked(nil, true)
	s.mu.Unlock()
	return err
}

func (s *rawStream) CloseRead() error {
	s.mu.Lock()
	preverror := s.read.err
	s.setReadErrorLocked(ECLOSED)
	s.clearReadBufferLocked()
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
	s.clearReadBufferLocked()
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
	preverror := s.closeWithErrorLocked(err, true)
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
	return s.owner.conn.LocalAddr()
}

func (s *rawStream) RemoteAddr() net.Addr {
	return s.owner.conn.RemoteAddr()
}

// rawStreamQueue is a ring-buffer queue of rawStream pointers
type rawStreamQueue struct {
	buf  []*rawStream
	off  int
	size int
}

// push adds a pointer to the end of the queue, expanding it when necessary
func (q *rawStreamQueue) push(s *rawStream) {
	pos := q.off + q.size
	if q.size == len(q.buf) {
		dst := make([]*rawStream, minpow2(len(q.buf)+1))
		if pos == len(q.buf) {
			// queue doesn't wrap, simple copy
			copy(dst, q.buf)
		} else {
			// queue wraps, copy with two steps
			z := copy(dst, q.buf[q.off:])
			copy(dst[z:], q.buf[:q.size-z])
			pos = q.size
		}
		q.buf = dst
		q.off = 0
	} else if pos >= len(q.buf) {
		pos -= len(q.buf)
	}
	q.buf[pos] = s
	q.size++
}

// take removes a pointer from the front of the queue
func (q *rawStreamQueue) take() *rawStream {
	if q.size == 0 {
		return nil
	}
	stream := q.buf[q.off]
	q.buf[q.off] = nil
	q.off++
	q.size--
	if q.off == len(q.buf) || q.size == 0 {
		q.off = 0
	}
	return stream
}
