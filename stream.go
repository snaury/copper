package copper

import (
	"fmt"
	"io"
	"net"
	"sync"
)

// Stream represents a multiplexed copper stream
type Stream interface {
	// Read reads data from the stream
	Read(b []byte) (n int, err error)

	// Peek is like Read, but does not consume data
	Peek(b []byte) (n int, err error)

	// Write writes data to the stream
	Write(b []byte) (n int, err error)

	// Closes closes the stream, discarding any data
	Close() error

	// CloseWrite closes the write side of the connection
	CloseWrite() error

	// CloseWithError closes the stream with the specified error
	CloseWithError(err error) error

	// Flush returns when data has been processed by the remote side
	Flush() error

	// StreamID returns the stream id
	StreamID() int

	// TargetID returns the target id
	TargetID() int64

	// LocalAddr returns the local network address
	LocalAddr() net.Addr

	// RemoteAddr returns the remote network address
	RemoteAddr() net.Addr
}

var _ Stream = &rawStream{}

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

type rawStreamAddr struct {
	streamID int
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
	streamID   int
	targetID   int64
	owner      *rawConn
	flags      int
	mayread    sync.Cond
	readbuf    buffer
	readerror  error
	maywrite   sync.Cond
	writebuf   buffer
	writeerror error
	writeleft  int
	writenack  int
	reseterror error
	flushed    sync.Cond
	delayerror error
}

func newIncomingStream(owner *rawConn, frame openFrame, maxwrite int) *rawStream {
	s := &rawStream{
		streamID:  frame.streamID,
		targetID:  frame.targetID,
		owner:     owner,
		writeleft: maxwrite,
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

func newOutgoingStream(owner *rawConn, streamID int, targetID int64, maxwrite int) *rawStream {
	s := &rawStream{
		outgoing:  true,
		streamID:  streamID,
		targetID:  targetID,
		owner:     owner,
		writeleft: maxwrite,
	}
	s.mayread.L = &owner.lock
	s.maywrite.L = &owner.lock
	s.flushed.L = &owner.lock
	s.flags |= flagStreamNeedOpen
	return s
}

func (s *rawStream) isFullyClosed() bool {
	return s.flags&flagStreamBothEOF == flagStreamBothEOF
}

func (s *rawStream) Read(b []byte) (n int, err error) {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	for s.readbuf.len() == 0 {
		if s.readerror != nil {
			err = s.readerror
			return
		}
		s.mayread.Wait()
	}
	if len(b) > 0 {
		n = s.readbuf.read(b)
		if s.readerror != nil && s.readbuf.len() == 0 {
			// there will be no more data, return the error
			err = s.readerror
		}
		if s.flags&flagStreamNoOutgoingAcks == 0 {
			s.owner.addOutgoingAckLocked(s.streamID, n)
		}
	}
	return
}

func (s *rawStream) Peek(b []byte) (n int, err error) {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	for s.readbuf.len() == 0 {
		if s.readerror != nil {
			err = s.readerror
			return
		}
		s.mayread.Wait()
	}
	if len(b) > 0 {
		n = s.readbuf.peek(b)
	}
	return
}

func (s *rawStream) Write(b []byte) (n int, err error) {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	for n < len(b) {
		if s.writeerror != nil {
			err = s.writeerror
			return
		}
		if s.writeleft == 0 {
			s.maywrite.Wait()
			continue
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

func (s *rawStream) processDataFrameLocked(frame dataFrame) error {
	if s.flags&flagStreamSeenEOF != 0 {
		return &errorWithReason{
			error:  fmt.Errorf("stream 0x%08x cannot have DATA after EOF"),
			reason: EINVALIDSTREAM,
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
	if s.delayerror == ECLOSED {
		// ECLOSED is special, it translated to a normal EOF
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
	if s.writebuf.len() > 0 || s.writenack > 0 {
		s.flushed.Broadcast()
	}
	s.writebuf.clear()
	s.writenack = 0
	return nil
}

func (s *rawStream) processWindowFrameLocked(frame windowFrame) error {
	if frame.increment <= 0 {
		return &errorWithReason{
			error:  fmt.Errorf("stream 0x%08x received invalid increment %d", s.streamID, frame.increment),
			reason: EINVALIDFRAME,
		}
	}
	if s.writeerror == nil {
		if s.writeleft == 0 {
			s.maywrite.Broadcast()
		}
		s.writeleft += frame.increment
	}
	if s.flags&flagStreamNoIncomingAcks == 0 {
		s.writenack -= frame.increment
		if s.writebuf.len() == 0 && s.writenack <= 0 {
			s.flushed.Broadcast()
		}
	}
	return nil
}

func (s *rawStream) outgoingFramesLocked(inframes []frame, maxsize int) (frames []frame, total int) {
	frames = inframes
	for {
		if s.flags&flagStreamDiscard == flagStreamDiscard && s.writebuf.len() == 0 {
			// If stream was opened, and then immediately closed, then we don't
			// have to send any frames at all, just pretend it all happened
			// already and move on.
			s.flags = flagStreamSentEOF | flagStreamSeenEOF | flagStreamSentReset
			break
		} else if s.outgoingSendOpen() {
			s.flags &^= flagStreamNeedOpen
			n := s.writebuf.len()
			if n > maxOpenFramePayloadSize {
				n = maxOpenFramePayloadSize
			}
			if n > maxsize {
				n = maxsize
			}
			var data []byte
			if n > 0 {
				data = make([]byte, n)
				s.writebuf.read(data)
				if s.flags&flagStreamNoIncomingAcks == 0 {
					s.writenack += len(data)
				}
			}
			frames = append(frames, openFrame{
				flags:    s.outgoingFlags(),
				streamID: s.streamID,
				targetID: s.targetID,
				data:     data,
			})
			maxsize -= len(data)
			total += len(data)
		} else if s.outgoingSendReset() {
			s.flags &^= flagStreamNeedReset
			s.flags |= flagStreamSentReset
			frames = append(frames, errorToResetFrame(
				s.outgoingFlags(),
				s.streamID,
				s.reseterror,
			))
		} else if s.writebuf.len() == 0 && s.flags&flagStreamNeedEOF != 0 {
			s.flags &^= flagStreamNeedEOF
			s.flags |= flagStreamSentEOF
			frames = append(frames, dataFrame{
				flags:    flagFin,
				streamID: s.streamID,
			})
			break
		} else {
			n := s.writebuf.len()
			if n > maxFramePayloadSize {
				n = maxFramePayloadSize
			}
			if n > maxsize {
				n = maxsize
			}
			if n == 0 {
				// there's nothing we can send
				break
			}
			data := make([]byte, n)
			s.writebuf.read(data)
			if s.flags&flagStreamNoIncomingAcks == 0 {
				s.writenack += len(data)
			}
			frames = append(frames, dataFrame{
				flags:    s.outgoingFlags(),
				streamID: s.streamID,
				data:     data,
			})
			maxsize -= len(data)
			total += len(data)
		}
	}
	return
}

func (s *rawStream) outgoingSendOpen() bool {
	return s.flags&flagStreamNeedOpen != 0
}

func (s *rawStream) outgoingSendReset() bool {
	if s.flags&flagStreamNeedReset != 0 {
		// we have a RESET frame pending
		if s.flags&flagStreamSeenEOF != 0 {
			// we have seen EOF: the other side cannot send us any data
			if s.reseterror == ECLOSED {
				// if the error was ECLOSED we don't need RESET at all
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

func (s *rawStream) outgoingSendEOF() bool {
	if s.writebuf.len() == 0 && s.flags&flagStreamNeedEOF != 0 {
		// we have no data and need to send EOF
		if s.flags&flagStreamNeedReset != 0 {
			// however there's an active reset as well
			if s.reseterror == ECLOSED {
				// errors like ECLOSED are translated into io.EOF, so it's ok
				// to send EOF even if we have a pending RESET.
				return true
			}
			// must send RESET before sending EOF
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
		// After sending EOF we cannot send any more data and will not
		// receive acknowledgements for any data we have already sent
		if s.writenack > 0 {
			s.flushed.Broadcast()
		}
		s.writenack = 0
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
	return s.writebuf.len() > 0
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
		if s.writeleft == 0 {
			s.maywrite.Broadcast()
		}
		s.writeleft = 0
		s.flags |= flagStreamNeedEOF
		s.owner.addOutgoingCtrlLocked(s.streamID)
	}
}

func (s *rawStream) closeWithErrorLocked(err error) error {
	if err == nil {
		err = ECLOSED
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

func (s *rawStream) Close() error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	return s.closeWithErrorLocked(nil)
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
	return s.closeWithErrorLocked(err)
}

func (s *rawStream) Flush() error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	for s.writebuf.len() > 0 || s.writenack > 0 {
		s.flushed.Wait()
	}
	return s.writeerror
}

func (s *rawStream) StreamID() int {
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

func isClientStreamID(streamID int) bool {
	return streamID&1 == 1
}

func isServerStreamID(streamID int) bool {
	return streamID&1 == 0
}
