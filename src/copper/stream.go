package copper

import (
	"fmt"
	"io"
	"sync"
)

// Stream represents a copper stream of data
type Stream interface {
	// Read reads data from the stream
	Read(b []byte) (n int, err error)

	// Write writes data to the stream
	Write(b []byte) (n int, err error)

	// Closes closes the stream, discarding any data
	Close() error

	// CloseRead closes the read side of the connection
	CloseRead() error

	// CloseWrite closes the write side of the connection
	CloseWrite() error

	// CloseWithError closes the stream with the specified error
	CloseWithError(err error) error

	// BufferedRead returns the number of bytes that can be read from the current buffer.
	BufferedRead() int

	// Peek returns currently available bytes without advancing the reader.
	// The bytes stop being valid at the next read call.
	Peek() ([]byte, error)

	// Consume discards up to the specified number of bytes
	Consume(n int) (int, error)

	// BufferedWrite returns the number of bytes that have been written into the current buffer.
	BufferedWrite() int

	// AvailableWrite returns how many bytes are unused in the buffer.
	AvailableWrite() int

	// Flush returns when all written bytes have been acknowledged
	Flush() error
}

type buffer struct {
	buf []byte
	off int
}

func (b *buffer) len() int {
	return len(b.buf) - b.off
}

func (b *buffer) peek() []byte {
	if b.off != 0 {
		m := copy(b.buf, b.buf[b.off:])
		b.buf = b.buf[:m]
		b.off = 0
	}
	return b.buf
}

func (b *buffer) grow(n int) []byte {
	m := b.len()
	if len(b.buf)+n > cap(b.buf) {
		if m+n <= cap(b.buf)/2 {
			copy(b.buf, b.buf[b.off:])
			b.buf = b.buf[:m]
		} else {
			buf := make([]byte, 2*cap(b.buf)+n)
			copy(buf, b.buf[b.off:])
			b.buf = buf
		}
		b.off = 0
	}
	b.buf = b.buf[:b.off+m+n]
	return b.buf[b.off+m:]
}

func (b *buffer) take(dst []byte) int {
	n := copy(dst, b.buf[b.off:])
	b.off += n
	if b.off == len(b.buf) {
		b.buf = b.buf[:0]
		b.off = 0
	}
	return n
}

func (b *buffer) clear() {
	b.buf = nil
	b.off = 0
}

const (
	flagStreamSentEOF   = 1
	flagStreamSeenEOF   = 2
	flagStreamBothEOF   = flagStreamSentEOF | flagStreamSeenEOF
	flagStreamSentReset = 4
	flagStreamSeenReset = 8
	flagStreamNeedOpen  = 16
	flagStreamNeedReset = 32
	flagStreamNeedEOF   = 64
	// we may reuse local streams if we have seen a RESET from the other side
	// otherwise when we send OPEN we may not now if RESET is already on the
	// wire, which would incorrectly reset a new stream, even though it was
	// for an earlier stream.
	flagStreamLocalFullyClosed = flagStreamSentEOF | flagStreamSeenEOF | flagStreamSeenReset
	// we may forget remote streams if we have sent a RESET to the other side
	flagStreamRemoteFullyClosed = flagStreamSentEOF | flagStreamSeenEOF | flagStreamSentReset
)

type stream struct {
	flags      int
	streamID   int
	targetID   int64
	owner      *rawConn
	mayread    sync.Cond
	readbuf    buffer
	readerror  error
	maywrite   sync.Cond
	writebuf   buffer
	writeerror error
	writeleft  int
	reseterror error
}

func (s *stream) isFullyClosed() bool {
	if s.streamID&1 != 0 {
		// this is a local stream
		return s.flags&flagStreamLocalFullyClosed == flagStreamLocalFullyClosed
	}
	// this is a remote stream
	return s.flags&flagStreamRemoteFullyClosed == flagStreamRemoteFullyClosed
}

func (s *stream) maybeScheduleReset() bool {
	// may only be called when readerror != nil
	if s.streamID&1 == 0 {
		// this is a remote stream
		if s.flags&flagStreamRemoteFullyClosed == flagStreamBothEOF && s.reseterror == nil {
			s.reseterror = ECLOSED
			s.flags |= flagStreamNeedReset
			s.owner.addOutgoingDataLocked(s.streamID)
			return true
		}
	}
	return false
}

func newIncomingStream(owner *rawConn, frame openFrame) *stream {
	s := &stream{
		streamID:  frame.streamID,
		targetID:  frame.targetID,
		owner:     owner,
		writeleft: 0,
	}
	s.mayread.L = &owner.lock
	s.maywrite.L = &owner.lock
	s.readbuf.buf = frame.data
	if frame.flags&flagFin != 0 {
		s.flags |= flagStreamSeenEOF
		s.readerror = io.EOF
	}
	return s
}

func (s *stream) Read(b []byte) (n int, err error) {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	for n < len(b) {
		for s.readbuf.len() == 0 {
			if s.readerror != nil {
				err = s.readerror
				return
			}
			s.mayread.Wait()
		}
		taken := s.readbuf.take(b[n:])
		s.owner.addOutgoingAckLocked(s.streamID, taken)
		n += taken
	}
	if s.readerror != nil && s.readbuf.len() == 0 {
		// there will be no more data, return the error
		err = s.readerror
	}
	return
}

func (s *stream) processDataFrameLocked(frame dataFrame) error {
	if s.flags&flagStreamSeenEOF != 0 {
		return &errorWithReason{
			error:  fmt.Errorf("stream 0x%08x cannot have data after a EOF"),
			reason: EINVALIDSTREAM,
		}
	}
	wakeup := false
	if s.readerror == nil && len(frame.data) > 0 {
		if s.readbuf.len() == 0 {
			wakeup = true
		}
		copy(s.readbuf.grow(len(frame.data)), frame.data)
	}
	if frame.flags&flagFin != 0 {
		s.flags |= flagStreamSeenEOF
		if s.readerror == nil {
			s.readerror = io.EOF
			wakeup = true
		}
		s.maybeScheduleReset()
	}
	if wakeup {
		s.mayread.Broadcast()
	}
	return nil
}

func (s *stream) Write(b []byte) (n int, err error) {
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
		copy(s.writebuf.grow(taken), b[n:])
		s.writeleft -= taken
		s.owner.addOutgoingDataLocked(s.streamID)
		n += taken
	}
	return
}

func (s *stream) processResetFrameLocked(frame resetFrame) error {
	s.flags |= flagStreamSeenReset
	if s.writeerror == nil {
		s.writeerror = frame.toError()
		s.maywrite.Broadcast()
	}
	if frame.flags&flagFin != 0 {
		s.flags |= flagStreamSeenEOF
		if s.readerror == nil {
			s.readerror = io.EOF
			s.mayread.Broadcast()
		}
		s.maybeScheduleReset()
	}
	return nil
}

func (s *stream) incrementWindowLocked(increment int) {
	old := s.writeleft
	s.writeleft = old + increment
	if old == 0 {
		s.maywrite.Broadcast()
	}
}

func (s *stream) outgoingFramesLocked(inframes []frame, maxsize int) (frames []frame, total int, active bool) {
	frames = inframes
	for {
		if s.flags&flagStreamNeedOpen != 0 {
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
				s.writebuf.take(data)
			}
			frames = append(frames, openFrame{
				flags:    s.outgoingFlags(),
				streamID: s.streamID,
				targetID: s.targetID,
				data:     data,
			})
			maxsize -= len(data)
			total += len(data)
		} else if s.flags&flagStreamNeedReset != 0 {
			s.flags &^= flagStreamNeedReset
			s.flags |= flagStreamSentReset
			frames = append(frames, errorToResetFrame(
				s.outgoingFlags(),
				s.streamID,
				s.reseterror,
			))
		} else {
			n := s.writebuf.len()
			if n > maxFramePayloadSize {
				n = maxFramePayloadSize
			}
			if n > maxsize {
				n = maxsize
			}
			var data []byte
			if n > 0 {
				data = make([]byte, n)
				s.writebuf.take(data)
			}
			flags := s.outgoingFlags()
			if len(data) == 0 && flags == 0 {
				// there's actually nothing to send
				break
			}
			frames = append(frames, dataFrame{
				flags:    flags,
				streamID: s.streamID,
				data:     data,
			})
			maxsize -= len(data)
			total += len(data)
		}
	}
	active = s.outgoingActive()
	return
}

func (s *stream) outgoingFlags() uint8 {
	var flags uint8
	if s.flags&flagStreamNeedEOF != 0 && s.writebuf.len() == 0 {
		// need to send eof and no buffered data
		s.flags &^= flagStreamNeedEOF
		s.flags |= flagStreamSentEOF
		flags |= flagFin
	}
	return flags
}

func (s *stream) outgoingActive() bool {
	return s.writebuf.len() > 0 || s.flags&(flagStreamNeedOpen|flagStreamNeedEOF|flagStreamNeedReset) != 0
}

func (s *stream) closeWithErrorLocked(err error) {
	wakeup := false
	if s.writeerror == nil {
		s.flags |= flagStreamNeedEOF
		s.writeerror = err
		s.maywrite.Broadcast()
		wakeup = true
	}
	if s.readerror == nil {
		s.readerror = err
		s.mayread.Broadcast()
		if s.reseterror == nil {
			s.reseterror = err
			s.flags |= flagStreamNeedReset
			wakeup = true
		}
	}
	if wakeup {
		s.owner.addOutgoingDataLocked(s.streamID)
	}
}

func (s *stream) Close() error {
	return s.CloseWithError(ECLOSED)
}

func (s *stream) CloseRead() (err error) {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	err = s.readerror
	if err == nil {
		s.readbuf.clear()
		s.readerror = ECLOSED
		s.mayread.Broadcast()
		if s.reseterror == nil {
			s.reseterror = ECLOSED
			s.flags |= flagStreamNeedReset
			s.owner.addOutgoingDataLocked(s.streamID)
		}
	}
	return
}

func (s *stream) CloseWrite() (err error) {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	err = s.writeerror
	if err == nil {
		s.writeerror = ECLOSED
		s.flags |= flagStreamNeedEOF
		s.owner.addOutgoingDataLocked(s.streamID)
		s.maywrite.Broadcast()
	}
	return
}

func (s *stream) CloseWithError(err error) (result error) {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	if s.writeerror != nil {
		result = s.writeerror
	} else if s.readerror != nil {
		result = s.readerror
	} else if s.reseterror != nil {
		result = s.reseterror
	}
	s.closeWithErrorLocked(err)
	return
}
