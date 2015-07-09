package copper

import (
	"fmt"
	"io"
	"net"
	"time"
)

const (
	flagStreamSentEOF   = 0x01
	flagStreamSeenEOF   = 0x02
	flagStreamBothEOF   = flagStreamSentEOF | flagStreamSeenEOF
	flagStreamSentReset = 0x04
	flagStreamNeedOpen  = 0x10
	flagStreamNeedEOF   = 0x20
	flagStreamNeedReset = 0x40
	flagStreamDiscard   = flagStreamNeedOpen | flagStreamNeedEOF | flagStreamNeedReset
)

type rawStream struct {
	outgoing   bool
	streamID   uint32
	targetID   int64
	owner      *rawConn
	flags      int
	mayread    condWithDeadline
	readbuf    buffer
	readerror  error
	readwindow int
	maywrite   condWithDeadline
	writebuf   buffer
	writeerror error
	writeleft  int
	writewire  int
	writenack  int
	writefail  int
	reseterror error
	flushed    condWithDeadline
	acked      condWithDeadline
}

var _ Stream = &rawStream{}

func newIncomingStream(owner *rawConn, frame *openFrame, readwindow, writewindow int) *rawStream {
	s := &rawStream{
		streamID:   frame.streamID,
		targetID:   frame.targetID,
		owner:      owner,
		readwindow: readwindow,
		writeleft:  writewindow,
	}
	s.mayread.init(&owner.lock)
	s.maywrite.init(&owner.lock)
	s.flushed.init(&owner.lock)
	s.acked.init(&owner.lock)
	if len(frame.data) > 0 {
		s.readbuf.write(frame.data)
	}
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
	s.mayread.init(&owner.lock)
	s.maywrite.init(&owner.lock)
	s.flushed.init(&owner.lock)
	s.acked.init(&owner.lock)
	s.flags |= flagStreamNeedOpen
	return s
}

func (s *rawStream) canReceive() bool {
	return s.readerror == nil
}

func (s *rawStream) isFullyClosed() bool {
	return s.flags&flagStreamBothEOF == flagStreamBothEOF && s.readbuf.len() == 0 && s.writenack <= 0
}

func (s *rawStream) clearReadBuffer() {
	if s.readbuf.len() > 0 {
		s.readbuf.clear()
		s.owner.cleanupStreamLocked(s)
	}
}

func (s *rawStream) clearWriteBuffer() {
	// Any unacknowledged data will not be acknowledged
	s.writefail += s.writebuf.len() - s.writewire
	s.writebuf.clear()
	s.writewire = 0
	s.writefail += s.writenack
	s.writenack = 0
	s.flushed.Broadcast()
	s.acked.Broadcast()
}

func (s *rawStream) setReadDeadlineLocked(t time.Time) {
	s.mayread.setDeadline(t)
}

func (s *rawStream) setWriteDeadlineLocked(t time.Time) {
	s.maywrite.setDeadline(t)
	s.flushed.setDeadline(t)
	s.acked.setDeadline(t)
}

func (s *rawStream) waitReadLocked() error {
	for s.readbuf.len() == 0 {
		if s.readerror != nil {
			return s.readerror
		}
		err := s.mayread.Wait()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *rawStream) waitWriteLocked() error {
	for s.writeerror == nil && s.writeleft <= 0 {
		err := s.maywrite.Wait()
		if err != nil {
			return err
		}
	}
	return s.writeerror
}

func (s *rawStream) waitFlushLocked() error {
	for s.writebuf.len() > 0 {
		if s.reseterror != nil {
			break
		}
		err := s.flushed.Wait()
		if err != nil {
			return err
		}
	}
	return s.writeerror
}

func (s *rawStream) waitAckLocked(n int) error {
	for s.writenack+s.writebuf.len()-s.writewire > n {
		if s.reseterror != nil {
			break
		}
		err := s.acked.Wait()
		if err != nil {
			return err
		}
	}
	return s.writeerror
}

func (s *rawStream) processDataFrameLocked(frame *dataFrame) error {
	if s.flags&flagStreamSeenEOF != 0 {
		return copperError{
			error: fmt.Errorf("stream 0x%08x cannot have DATA after EOF"),
			code:  EINVALIDSTREAM,
		}
	}
	if s.readbuf.len()+len(frame.data) > s.readwindow {
		return copperError{
			error: fmt.Errorf("stream 0x%08x received %d+%d bytes, which is more than %d bytes window", frame.streamID, s.readbuf.len(), len(frame.data), s.readwindow),
			code:  EWINDOWOVERFLOW,
		}
	}
	if len(frame.data) > 0 {
		if s.readerror == nil {
			s.readbuf.write(frame.data)
			s.mayread.Broadcast()
		}
	}
	if frame.flags&flagFin != 0 {
		s.flags |= flagStreamSeenEOF
		s.setReadError(io.EOF)
		s.owner.cleanupStreamLocked(s)
	}
	return nil
}

func (s *rawStream) processResetFrameLocked(frame *resetFrame) error {
	s.setWriteError(frame.err)
	s.clearWriteBuffer()
	if frame.flags&flagFin != 0 {
		reset := frame.err
		if reset == ECLOSED {
			// ECLOSED is special, it translates to a normal EOF
			reset = io.EOF
		}
		s.flags |= flagStreamSeenEOF
		s.setReadError(reset)
		s.owner.cleanupStreamLocked(s)
	}
	return nil
}

func (s *rawStream) changeWindowLocked(diff int) {
	if s.writeerror == nil {
		s.writeleft += diff
		if s.activeData() {
			s.owner.addOutgoingDataLocked(s.streamID)
		}
		if s.writeleft > 0 {
			s.maywrite.Broadcast()
		}
	}
}

func (s *rawStream) processWindowFrameLocked(frame *windowFrame) error {
	if frame.increment <= 0 {
		return copperError{
			error: fmt.Errorf("stream 0x%08x received invalid increment %d", s.streamID, frame.increment),
			code:  EINVALIDFRAME,
		}
	}
	if frame.flags&flagAck != 0 {
		s.writenack -= int(frame.increment)
		s.acked.Broadcast() // TODO: split into acked full and acked partial
		s.owner.cleanupStreamLocked(s)
	}
	if frame.flags&flagInc != 0 {
		s.changeWindowLocked(int(frame.increment))
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
		s.owner.writeleft -= len(data)
		s.writenack += len(data)
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
		err := s.owner.writeFrameLocked(&openFrame{
			streamID: s.streamID,
			flags:    s.outgoingFlags(),
			targetID: s.targetID,
			data:     data,
		})
		if err != nil {
			return err
		}
		s.writebuf.discard(s.writewire)
		s.writewire = 0
		if s.writebuf.len() == 0 {
			s.flushed.Broadcast()
		}
	}
	if s.outgoingSendReset() {
		var flags uint8
		reset := s.reseterror
		if reset == nil {
			// The only way we may end up with reseterror being nil, but with
			// an outgoing RESET pending, is when we haven't seen a EOF yet,
			// but either CloseRead() or CloseReadError() was called. In both
			// of those cases the error is in readerror.
			reset = s.readerror
		}
		if s.writebuf.len() == 0 && s.flags&flagStreamNeedEOF != 0 {
			// This RESET closes both sides of the stream, otherwise it only
			// closes the read side and write side is delayed until we need to
			// send EOF.
			s.flags &^= flagStreamNeedReset
			s.flags &^= flagStreamNeedEOF
			s.flags |= flagStreamSentEOF
			flags |= flagFin
			s.owner.cleanupStreamLocked(s)
		}
		// N.B.: we may send RESET twice. First without EOF, to stop the other
		// side from sending us more data. Second with EOF, after sending all
		// our pending data, to convey the error message to the other side.
		s.flags |= flagStreamSentReset
		err := s.owner.writeFrameLocked(&resetFrame{
			streamID: s.streamID,
			flags:    flags,
			err:      reset,
		})
		if err != nil {
			return err
		}
	}
	if s.writebuf.len() == 0 && s.flags&flagStreamNeedEOF != 0 {
		s.flags &^= flagStreamNeedEOF
		s.flags |= flagStreamSentEOF
		err := s.owner.writeFrameLocked(&dataFrame{
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
		err := s.owner.writeFrameLocked(&dataFrame{
			streamID: s.streamID,
			flags:    s.outgoingFlags(),
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
		} else if s.outgoingSendReset() {
			// sending all data unblocked a pending RESET
			s.owner.addOutgoingCtrlLocked(s.streamID)
		}
		if s.writebuf.len() == 0 {
			s.flushed.Broadcast()
		}
	}
	return nil
}

func (s *rawStream) outgoingSendOpen() bool {
	return s.flags&flagStreamNeedOpen != 0
}

func (s *rawStream) outgoingSendReset() bool {
	if s.flags&flagStreamBothEOF == flagStreamBothEOF {
		// both sides already closed, don't need to send anything
		return false
	}
	if s.flags&flagStreamNeedReset != 0 {
		// we have a RESET frame pending
		if s.writebuf.len() == 0 && s.flags&flagStreamNeedEOF != 0 {
			// we need to send EOF too (closing both sides)
			if s.reseterror == nil || s.reseterror == ECLOSED {
				// if the error was ECLOSED we may send DATA with EOF
				if s.flags&flagStreamSeenEOF == 0 {
					// but we haven't seen EOF yet, so we need RESET
					return true
				}
				s.flags &^= flagStreamNeedReset
				return false
			}
			return true
		}
		if s.flags&flagStreamSeenEOF != 0 {
			// we have seen EOF, so this RESET is for the write side only
			if s.reseterror == nil || s.reseterror == ECLOSED {
				// if the error was ECLOSED we no longer need RESET
				s.flags &^= flagStreamNeedReset
				return false
			}
			// we must delay RESET until we need to send EOF
			return false
		}
		if s.flags&flagStreamSentReset != 0 {
			// we have already sent RESET for the read side
			return false
		}
		return true
	}
	return false
}

func (s *rawStream) outgoingSendEOF() bool {
	if s.writebuf.len() == s.writewire && s.flags&flagStreamNeedEOF != 0 {
		// we have no data and need to send EOF
		if s.flags&flagStreamNeedReset != 0 {
			// however there's an active reset as well
			if s.reseterror == nil || s.reseterror == ECLOSED {
				// errors like ECLOSED are translated into io.EOF, so
				// it's ok to send EOF even if we have a pending RESET.
				return true
			}
			// must send EOF with a RESET
			return false
		}
		return true
	}
	return false
}

func (s *rawStream) outgoingFlags() uint8 {
	var flags uint8
	if s.outgoingSendEOF() {
		s.flags &^= flagStreamNeedEOF
		s.flags |= flagStreamSentEOF
		flags |= flagFin
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
	pending := s.writebuf.len() - s.writewire
	if s.writeleft >= 0 {
		return pending > 0
	}
	return pending+s.writeleft > 0
}

func (s *rawStream) resetReadSide() {
	if s.flags&flagStreamSeenEOF == 0 {
		s.flags |= flagStreamNeedReset
		if s.outgoingSendReset() {
			s.owner.addOutgoingCtrlLocked(s.streamID)
		}
	}
}

func (s *rawStream) resetBothSides() {
	if s.flags&flagStreamBothEOF != flagStreamBothEOF {
		s.flags |= flagStreamNeedReset
		if s.outgoingSendReset() {
			s.owner.addOutgoingCtrlLocked(s.streamID)
		}
	}
}

func (s *rawStream) setReadError(err error) {
	if s.readerror == nil {
		s.readerror = err
		s.mayread.Broadcast()
	}
}

func (s *rawStream) setWriteError(err error) {
	if s.writeerror == nil {
		s.writeerror = err
		s.writeleft = 0
		s.flags |= flagStreamNeedEOF
		s.owner.addOutgoingCtrlLocked(s.streamID)
		s.maywrite.Broadcast()
	}
}

func (s *rawStream) closeWithErrorLocked(err error, closed bool) error {
	if err == nil || err == io.EOF {
		err = ECLOSED
	}
	preverror := s.reseterror
	if preverror == nil {
		s.reseterror = err
		s.setReadError(err)
		s.setWriteError(err)
		s.flushed.Broadcast()
		s.acked.Broadcast()
	}
	if closed {
		s.resetBothSides()
		s.clearReadBuffer()
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
		s.owner.addOutgoingAckLocked(s.streamID, n)
		if s.readbuf.len() == 0 {
			s.owner.cleanupStreamLocked(s)
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
	if n > 0 {
		s.owner.addOutgoingAckLocked(s.streamID, n)
		if s.readbuf.len() == 0 {
			s.owner.cleanupStreamLocked(s)
		}
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

func (s *rawStream) WaitAck() (int, error) {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	err := s.waitAckLocked(0)
	if err != nil {
		return s.writefail + s.writenack + s.writebuf.len() - s.writewire, err
	}
	return 0, nil
}

func (s *rawStream) WaitAckAny(n int) (int, error) {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	var err error
	if n > 0 {
		err = s.waitAckLocked(n - 1)
	} else {
		err = s.writeerror
	}
	return s.writefail + s.writenack + s.writebuf.len() - s.writewire, err
}

func (s *rawStream) WaitWriteClosed() error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	for s.writeerror == nil {
		s.maywrite.Wait()
	}
	return s.writeerror
}

func (s *rawStream) Close() error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	return s.closeWithErrorLocked(nil, true)
}

func (s *rawStream) CloseRead() error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	preverror := s.readerror
	s.setReadError(ECLOSED)
	s.resetReadSide()
	s.clearReadBuffer()
	return preverror
}

func (s *rawStream) CloseReadError(err error) error {
	if err == nil || err == io.EOF {
		err = ECLOSED
	}
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	preverror := s.readerror
	s.setReadError(err)
	s.resetReadSide()
	s.clearReadBuffer()
	return preverror
}

func (s *rawStream) CloseWrite() error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	preverror := s.writeerror
	s.setWriteError(ECLOSED)
	return preverror
}

func (s *rawStream) CloseWithError(err error) error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	return s.closeWithErrorLocked(err, true)
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
	return &StreamAddr{
		NetAddr:  s.owner.conn.LocalAddr(),
		StreamID: s.streamID,
		TargetID: s.targetID,
		Outgoing: !s.outgoing,
	}
}

func (s *rawStream) RemoteAddr() net.Addr {
	return &StreamAddr{
		NetAddr:  s.owner.conn.RemoteAddr(),
		StreamID: s.streamID,
		TargetID: s.targetID,
		Outgoing: s.outgoing,
	}
}

func isClientStreamID(streamID uint32) bool {
	return streamID&1 == 1
}

func isServerStreamID(streamID uint32) bool {
	return streamID&1 == 0
}
