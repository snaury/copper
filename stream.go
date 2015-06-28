package copper

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Stream represents a multiplexed copper stream
type Stream interface {
	// Read reads data from the stream
	Read(b []byte) (n int, err error)

	// Peek is similar to Read, but returns available data without consuming
	// it. The Returned buffer is only valid until the next Peek, Read or
	// Discard call. When returned buffer constitutes all available data on
	// the stream the error (e.g. io.EOF) is also returned.
	Peek() (b []byte, err error)

	// Discard throws away up to n bytes of data, as if consumed by Read, and
	// returns the number of bytes that have been discarded. This call does not
	// block and returns 0 if there is no data in the buffer.
	Discard(n int) int

	// Write writes data to the stream
	Write(b []byte) (n int, err error)

	// Flush returns when data has been processed by the remote side
	Flush() error

	// Closes closes the stream, discarding any data
	Close() error

	// CloseWrite closes the write side of the connection
	CloseWrite() error

	// CloseWithError closes the stream with the specified error
	CloseWithError(err error) error

	// SetDeadline sets both read and write deadlines
	SetDeadline(t time.Time) error

	// SetReadDeadline sets a read deadline
	SetReadDeadline(t time.Time) error

	// SetWriteDeadline sets a write deadline
	SetWriteDeadline(t time.Time) error

	// StreamID returns the stream id
	StreamID() uint32

	// TargetID returns the target id
	TargetID() int64

	// LocalAddr returns the local network address
	LocalAddr() net.Addr

	// RemoteAddr returns the remote network address
	RemoteAddr() net.Addr
}

var _ net.Conn = Stream(nil)

const (
	flagStreamSentEOF        = 1
	flagStreamSeenEOF        = 2
	flagStreamBothEOF        = flagStreamSentEOF | flagStreamSeenEOF
	flagStreamSentReset      = 4
	flagStreamSeenReset      = 8
	flagStreamNeedOpen       = 16
	flagStreamNeedEOF        = 32
	flagStreamNeedReset      = 64
	flagStreamDiscard        = flagStreamNeedOpen | flagStreamNeedEOF | flagStreamNeedReset
	flagStreamNoOutgoingAcks = flagStreamSeenEOF | flagStreamSentReset
	flagStreamNoIncomingAcks = flagStreamSentEOF | flagStreamSeenReset
)

type expiration struct {
	t       *time.Timer
	cond    *sync.Cond
	expired bool
}

func newExpiration(cond *sync.Cond, d time.Duration) *expiration {
	e := &expiration{
		cond: cond,
	}
	e.t = time.AfterFunc(d, e.callback)
	return e
}

func (e *expiration) callback() {
	e.cond.L.Lock()
	defer e.cond.L.Unlock()
	if !e.expired {
		e.expired = true
		e.cond.Broadcast()
	}
}

func (e *expiration) stop() bool {
	if e.t != nil {
		return e.t.Stop()
	}
	return false
}

type rawStreamAddr struct {
	streamID uint32
	targetID int64
	netaddr  net.Addr
}

func (addr *rawStreamAddr) Network() string {
	return "copper"
}

func (addr *rawStreamAddr) String() string {
	if addr.streamID != 0 {
		return fmt.Sprintf("stream %d @ %s", addr.streamID, addr.netaddr.String())
	}
	return fmt.Sprintf("target %d @ %s", addr.targetID, addr.netaddr.String())
}

type rawStream struct {
	outgoing   bool
	streamID   uint32
	targetID   int64
	owner      *rawConn
	flags      int
	mayread    sync.Cond
	readbuf    buffer
	readerror  error
	readwindow int
	maywrite   sync.Cond
	writebuf   buffer
	writeerror error
	writeleft  int
	writenack  int
	writewire  int
	reseterror error
	flushed    sync.Cond
	delayerror error
	rdeadline  time.Time
	wdeadline  time.Time
	readexp    *expiration
	writeexp   *expiration
	flushexp   *expiration
}

var _ Stream = &rawStream{}

func newIncomingStream(owner *rawConn, frame openFrame, readwindow, writewindow int) *rawStream {
	s := &rawStream{
		streamID:   frame.streamID,
		targetID:   frame.targetID,
		owner:      owner,
		readwindow: readwindow,
		writeleft:  writewindow,
	}
	s.mayread.L = &owner.lock
	s.maywrite.L = &owner.lock
	s.flushed.L = &owner.lock
	s.readbuf.buf = frame.data
	if frame.flags&flagFin != 0 {
		s.flags |= flagStreamSeenEOF
		s.readerror = io.EOF
	}
	return s
}

func newOutgoingStream(owner *rawConn, streamID uint32, targetID int64, readwindow, writewindow int) *rawStream {
	s := &rawStream{
		outgoing:   true,
		streamID:   streamID,
		targetID:   targetID,
		owner:      owner,
		readwindow: readwindow,
		writeleft:  writewindow,
	}
	s.mayread.L = &owner.lock
	s.maywrite.L = &owner.lock
	s.flushed.L = &owner.lock
	s.flags |= flagStreamNeedOpen
	return s
}

func (s *rawStream) writePending() int {
	return s.writebuf.len() - s.writewire
}

func (s *rawStream) isFullyClosed() bool {
	return s.flags&flagStreamBothEOF == flagStreamBothEOF
}

func (s *rawStream) setReadDeadlineLocked(t time.Time) {
	if s.rdeadline != t {
		if s.readexp != nil {
			s.readexp.stop()
			s.readexp = nil
		}
		s.rdeadline = t
		if !t.IsZero() {
			// Reads already waiting need to have a chance to timeout
			s.mayread.Broadcast()
		}
	}
}

func (s *rawStream) setWriteDeadlineLocked(t time.Time) {
	if s.wdeadline != t {
		if s.writeexp != nil {
			s.writeexp.stop()
			s.writeexp = nil
		}
		if s.flushexp != nil {
			s.flushexp.stop()
			s.flushexp = nil
		}
		s.wdeadline = t
		if !t.IsZero() {
			// Writes already waiting need to have a chance to timeout
			s.maywrite.Broadcast()
			s.flushed.Broadcast()
		}
	}
}

func (s *rawStream) waitReadLocked() error {
	for s.readbuf.len() == 0 {
		if s.readerror != nil {
			return s.readerror
		}
		if s.readexp == nil && !s.rdeadline.IsZero() {
			d := s.rdeadline.Sub(time.Now())
			if d <= 0 {
				return errTimeout
			}
			s.readexp = newExpiration(&s.mayread, d)
		}
		s.mayread.Wait()
		if s.readexp != nil && s.readexp.expired {
			return errTimeout
		}
	}
	return nil
}

func (s *rawStream) waitWriteLocked() error {
	for s.writeerror == nil && s.writeleft <= 0 {
		if s.writeexp == nil && !s.wdeadline.IsZero() {
			d := s.wdeadline.Sub(time.Now())
			if d <= 0 {
				return errTimeout
			}
			s.writeexp = newExpiration(&s.maywrite, d)
		}
		s.maywrite.Wait()
		if s.writeexp != nil && s.writeexp.expired {
			return errTimeout
		}
	}
	return s.writeerror
}

func (s *rawStream) waitFlushLocked() error {
	for s.writePending() > 0 || s.writenack > 0 {
		if s.flushexp == nil && !s.wdeadline.IsZero() {
			d := s.wdeadline.Sub(time.Now())
			if d <= 0 {
				return errTimeout
			}
			s.flushexp = newExpiration(&s.flushed, d)
		}
		s.flushed.Wait()
		if s.flushexp != nil && s.flushexp.expired {
			return errTimeout
		}
	}
	return s.writeerror
}

func (s *rawStream) processDataFrameLocked(frame dataFrame) error {
	if s.flags&flagStreamSeenEOF != 0 {
		return &copperError{
			error: fmt.Errorf("stream 0x%08x cannot have DATA after EOF"),
			code:  EINVALIDSTREAM,
		}
	}
	if s.readbuf.len()+len(frame.data) > s.readwindow {
		return &copperError{
			error: fmt.Errorf("stream 0x%08x received %d+%d bytes, which is more than %d bytes window", frame.streamID, s.readbuf.len(), len(frame.data), s.readwindow),
			code:  EWINDOWOVERFLOW,
		}
	}
	if s.readerror == nil && len(frame.data) > 0 {
		if s.readbuf.len() == 0 {
			s.mayread.Broadcast()
		}
		s.readbuf.write(frame.data)
	}
	if frame.flags&flagFin != 0 {
		s.flags |= flagStreamSeenEOF
		if s.delayerror == nil {
			s.delayerror = io.EOF
		}
		s.setReadError(s.delayerror)
		s.owner.clearOutgoingAckLocked(s.streamID)
	}
	return nil
}

func (s *rawStream) processResetFrameLocked(frame resetFrame) error {
	s.flags |= flagStreamSeenReset
	s.delayerror = frame.toError()
	if s.delayerror == ESTREAMCLOSED {
		// ESTREAMCLOSED is special, it translated to a normal EOF
		s.delayerror = io.EOF
	}
	if frame.flags&flagFin != 0 {
		s.flags |= flagStreamSeenEOF
		s.setReadError(s.delayerror)
		s.owner.clearOutgoingAckLocked(s.streamID)
	}
	s.setWriteError(frame.toError())
	// After sending RESET the remote will no longer accept data and won't send
	// acknowledgements, so we have to stop sending and wake up from Flush()
	if s.writePending() > 0 || s.writenack > 0 {
		s.flushed.Broadcast()
	}
	s.writebuf.clear()
	s.writenack = 0
	s.writewire = 0
	return nil
}

func (s *rawStream) changeWindowLocked(diff int) {
	if s.writeerror == nil {
		if diff > 0 {
			if s.writeleft <= 0 && s.writeleft+diff > 0 {
				s.maywrite.Broadcast()
			}
			if s.writeleft < 0 && s.writePending() > 0 {
				s.owner.addOutgoingDataLocked(s.streamID)
			}
		}
		s.writeleft += diff
	}
}

func (s *rawStream) processWindowFrameLocked(frame windowFrame) error {
	if frame.increment <= 0 {
		return &copperError{
			error: fmt.Errorf("stream 0x%08x received invalid increment %d", s.streamID, frame.increment),
			code:  EINVALIDFRAME,
		}
	}
	s.changeWindowLocked(int(frame.increment))
	if frame.flags&flagAck != 0 && s.flags&flagStreamNoIncomingAcks == 0 {
		s.writenack -= int(frame.increment)
		if s.writePending() == 0 && s.writenack <= 0 {
			s.flushed.Broadcast()
		}
	}
	return nil
}

func (s *rawStream) prepareDataLocked(maxpayload int) []byte {
	n := s.writebuf.len()
	if s.writeleft < 0 {
		// Incoming SETTINGS reduced our window and we have extra data in our
		// buffer, however we cannot send that extra data until there's a
		// window on the remote side.
		n += s.writeleft
	}
	if n > maxpayload {
		n = maxpayload
	}
	if n > s.owner.writeleft {
		n = s.owner.writeleft
	}
	if n > 0 {
		data := s.writebuf.current()
		if len(data) > n {
			data = data[:n]
		}
		if s.flags&flagStreamNoIncomingAcks == 0 {
			s.writenack += len(data)
		}
		s.owner.writeleft -= len(data)
		s.writewire = len(data)
		return data
	}
	return nil
}

func (s *rawStream) writeOutgoingCtrlLocked() error {
	if s.flags&flagStreamDiscard == flagStreamDiscard && s.writebuf.len() == 0 {
		// If stream was opened, and then immediately closed, then we don't
		// have to send any frames at all, just pretend it all happened
		// already and move on.
		s.flags = flagStreamSentEOF | flagStreamSeenEOF | flagStreamSentReset
		s.owner.cleanupStreamLocked(s)
		return nil
	}
	if s.outgoingSendOpen() {
		s.flags &^= flagStreamNeedOpen
		data := s.prepareDataLocked(maxOpenFramePayloadSize)
		err := s.owner.writeFrameLocked(openFrame{
			streamID: s.streamID,
			flags:    s.outgoingFlags(false),
			targetID: s.targetID,
			data:     data,
		})
		if err != nil {
			return err
		}
		s.writebuf.discard(s.writewire)
		s.writewire = 0
	}
	if s.outgoingSendReset() {
		s.flags &^= flagStreamNeedReset
		s.flags |= flagStreamSentReset
		err := s.owner.writeFrameLocked(errorToResetFrame(
			s.outgoingFlags(false),
			s.streamID,
			s.reseterror,
		))
		if err != nil {
			return err
		}
	}
	if s.writebuf.len() == 0 && s.flags&flagStreamNeedEOF != 0 {
		s.flags &^= flagStreamNeedEOF
		s.flags |= flagStreamSentEOF
		err := s.owner.writeFrameLocked(dataFrame{
			streamID: s.streamID,
			flags:    flagFin,
		})
		if err != nil {
			return err
		}
		s.owner.cleanupStreamLocked(s)
	}
	// after we return we no longer need to send control frames
	return nil
}

func (s *rawStream) writeOutgoingDataLocked() error {
	data := s.prepareDataLocked(maxDataFramePayloadSize)
	if len(data) > 0 {
		err := s.owner.writeFrameLocked(dataFrame{
			streamID: s.streamID,
			flags:    s.outgoingFlags(true),
			data:     data,
		})
		if err != nil {
			return err
		}
		s.writebuf.discard(s.writewire)
		s.writewire = 0
		if s.activeData() {
			// we have more data, make sure to re-register
			s.owner.addOutgoingDataLocked(s.streamID)
		}
	}
	return nil
}

func (s *rawStream) outgoingSendOpen() bool {
	return s.flags&flagStreamNeedOpen != 0
}

func (s *rawStream) outgoingSendReset() bool {
	if s.flags&flagStreamNeedReset != 0 {
		// we have a RESET frame pending
		if s.flags&flagStreamSeenEOF != 0 {
			// we have seen EOF: the other side cannot send us any data
			if s.reseterror == ESTREAMCLOSED {
				// if the error was ESTREAMCLOSED we don't need RESET at all
				s.flags &^= flagStreamNeedReset
				return false
			}
			if s.writebuf.len() == 0 && s.flags&flagStreamNeedEOF != 0 {
				// since we are about to EOF we might as well do it with RESET
				return true
			}
			// we may delay RESET until we need to send EOF
			return false
		}
		return true
	}
	return false
}

func (s *rawStream) outgoingSendEOF(isdata bool) bool {
	if s.writebuf.len() == s.writewire && s.flags&flagStreamNeedEOF != 0 {
		// we have no data and need to send EOF
		if s.flags&flagStreamNeedReset != 0 {
			// however there's an active reset as well
			if s.reseterror == ESTREAMCLOSED {
				// errors like ESTREAMCLOSED are translated into io.EOF, so
				// it's ok to send EOF even if we have a pending RESET.
				return true
			}
			// must send EOF with a RESET
			if isdata {
				s.owner.addOutgoingCtrlLocked(s.streamID)
			}
			return false
		}
		return true
	}
	return false
}

func (s *rawStream) outgoingFlags(isdata bool) uint8 {
	var flags uint8
	if s.outgoingSendEOF(isdata) {
		s.flags &^= flagStreamNeedEOF
		s.flags |= flagStreamSentEOF
		flags |= flagFin
		// After sending EOF we cannot send any more data and will not
		// receive acknowledgements for any data we have already sent
		if s.writenack > 0 {
			s.flushed.Broadcast()
		}
		s.writenack = 0
		s.owner.cleanupStreamLocked(s)
	}
	return flags
}

func (s *rawStream) activeCtrl() bool {
	if s.outgoingSendOpen() {
		return true
	}
	if s.outgoingSendReset() {
		return true
	}
	return s.writebuf.len() == 0 && s.flags&flagStreamNeedEOF != 0
}

func (s *rawStream) activeData() bool {
	if s.writeleft >= 0 {
		return s.writebuf.len() > 0
	}
	return s.writebuf.len()+s.writeleft > 0
}

func (s *rawStream) setReadError(err error) {
	if s.readerror == nil {
		s.readerror = err
		if s.readbuf.len() == 0 {
			s.mayread.Broadcast()
		}
	}
}

func (s *rawStream) setWriteError(err error) {
	if s.writeerror == nil {
		s.writeerror = err
		if s.writeleft <= 0 {
			s.maywrite.Broadcast()
		}
		s.writeleft = 0
		s.flags |= flagStreamNeedEOF
		s.owner.addOutgoingCtrlLocked(s.streamID)
	}
}

func (s *rawStream) closeWithErrorLocked(err error) error {
	if err == nil {
		err = ESTREAMCLOSED
	}
	preverror := s.reseterror
	if preverror == nil {
		s.reseterror = err
		s.setReadError(err)
		s.setWriteError(err)
		if !s.isFullyClosed() {
			s.flags |= flagStreamNeedReset
			s.owner.addOutgoingCtrlLocked(s.streamID)
		}
		s.readbuf.clear()
	}
	return preverror
}

func (s *rawStream) Read(b []byte) (n int, err error) {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	err = s.waitReadLocked()
	if err != nil {
		return
	}
	if len(b) > 0 {
		n = s.readbuf.read(b)
		if s.readerror != nil && s.readbuf.len() == 0 {
			// there will be no more data, return the error too
			err = s.readerror
		}
		if s.flags&flagStreamNoOutgoingAcks == 0 {
			s.owner.addOutgoingAckLocked(s.streamID, n)
		}
	}
	return
}

func (s *rawStream) Peek() (b []byte, err error) {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	err = s.waitReadLocked()
	if err != nil {
		return
	}
	return s.readbuf.current(), s.readerror
}

func (s *rawStream) Discard(n int) int {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	n = s.readbuf.discard(n)
	if n > 0 && s.flags&flagStreamNoOutgoingAcks == 0 {
		s.owner.addOutgoingAckLocked(s.streamID, n)
	}
	return n
}

func (s *rawStream) Write(b []byte) (n int, err error) {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	for n < len(b) {
		err = s.waitWriteLocked()
		if err != nil {
			return
		}
		taken := len(b) - n
		if taken > s.writeleft {
			taken = s.writeleft
		}
		s.writebuf.write(b[n : n+taken])
		s.writeleft -= taken
		s.owner.addOutgoingDataLocked(s.streamID)
		n += taken
	}
	return
}

func (s *rawStream) Flush() error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	return s.waitFlushLocked()
}

func (s *rawStream) Close() error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	return s.closeWithErrorLocked(nil)
}

func (s *rawStream) CloseWrite() error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	preverror := s.writeerror
	s.setWriteError(ESTREAMCLOSED)
	return preverror
}

func (s *rawStream) CloseWithError(err error) error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	return s.closeWithErrorLocked(err)
}

func (s *rawStream) SetDeadline(t time.Time) error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	s.setReadDeadlineLocked(t)
	s.setWriteDeadlineLocked(t)
	return nil
}

func (s *rawStream) SetReadDeadline(t time.Time) error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	s.setReadDeadlineLocked(t)
	return nil
}

func (s *rawStream) SetWriteDeadline(t time.Time) error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	s.setWriteDeadlineLocked(t)
	return nil
}

func (s *rawStream) StreamID() uint32 {
	return s.streamID
}

func (s *rawStream) TargetID() int64 {
	return s.targetID
}

func (s *rawStream) LocalAddr() net.Addr {
	addr := &rawStreamAddr{
		targetID: s.targetID,
		netaddr:  s.owner.conn.LocalAddr(),
	}
	if s.outgoing {
		addr.streamID = s.streamID
	}
	return addr
}

func (s *rawStream) RemoteAddr() net.Addr {
	addr := &rawStreamAddr{
		targetID: s.targetID,
		netaddr:  s.owner.conn.RemoteAddr(),
	}
	if !s.outgoing {
		addr.streamID = s.streamID
	}
	return addr
}

func isClientStreamID(streamID uint32) bool {
	return streamID&1 == 1
}

func isServerStreamID(streamID uint32) bool {
	return streamID&1 == 0
}
