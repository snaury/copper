package copper

import (
	"fmt"
	"io"
	"sync"
)

// Stream represents a multiplexed copper stream
type Stream interface {
	// Read reads data from the stream
	Read(b []byte) (n int, err error)

	// Write writes data to the stream
	Write(b []byte) (n int, err error)

	// Closes closes the stream, discarding any data
	Close() error

	// CloseWrite closes the write side of the connection
	CloseWrite() error

	// CloseWithError closes the stream with the specified error
	CloseWithError(err error) error

	// Flush returns when all writes have been prepared for the wire
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
	flushed    sync.Cond
}

func (s *stream) isFullyClosed() bool {
	return s.flags&flagStreamBothEOF == flagStreamBothEOF
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
	s.flushed.L = &owner.lock
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
	// RESET frame is an implicit EOF and destroys all unprocessed data
	s.flags |= flagStreamSeenEOF | flagStreamSeenReset
	if s.readerror == nil {
		s.readerror = frame.toError()
		if s.readbuf.len() == 0 {
			s.mayread.Broadcast()
		}
		s.readbuf.clear()
	}
	if s.writeerror == nil {
		s.writeerror = frame.toError()
		if s.writeleft == 0 {
			s.maywrite.Broadcast()
		}
		if s.writebuf.len() > 0 {
			s.writebuf.clear()
			s.flushed.Broadcast()
		}
		s.flags |= flagStreamNeedEOF
		s.owner.addOutgoingDataLocked(s.streamID)
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

func (s *stream) outgoingFramesLocked(inframes []frame, maxsize int) (frames []frame, total int) {
	frames = inframes
	for {
		if s.flags&flagStreamNeedReset != 0 {
			if s.flags&flagStreamNeedOpen != 0 {
				// if stream has been closed before we could send OPEN we may
				// as well discard everything and pretent it's been fully
				// closed as if we sent RESET and received a EOF.
				s.flags = flagStreamSeenEOF | flagStreamSentEOF | flagStreamSentReset
			} else {
				s.flags &^= flagStreamNeedEOF | flagStreamNeedReset
				s.flags |= flagStreamSentEOF | flagStreamSentReset
				frames = append(frames, errorToResetFrame(
					s.streamID,
					s.reseterror,
				))
			}
			break
		} else if s.flags&flagStreamNeedOpen != 0 {
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
				if s.writebuf.len() == 0 {
					s.flushed.Broadcast()
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
				s.flushed.Broadcast()
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

func (s *stream) active() bool {
	return s.writebuf.len() > 0 || s.flags&(flagStreamNeedOpen|flagStreamNeedEOF|flagStreamNeedReset) != 0
}

func (s *stream) closeWithErrorLocked(err error) error {
	preverror := s.reseterror
	if preverror == nil {
		s.reseterror = err
		if s.readerror == nil {
			s.readerror = err
			if s.readbuf.len() == 0 {
				s.mayread.Broadcast()
			}
			s.readbuf.clear()
		}
		if s.writeerror == nil {
			s.writeerror = err
			if s.writeleft == 0 {
				s.maywrite.Broadcast()
			}
			if s.writebuf.len() > 0 {
				s.writebuf.clear()
				s.flushed.Broadcast()
			}
		}
		if !s.isFullyClosed() {
			s.flags |= flagStreamNeedReset
			s.owner.addOutgoingDataLocked(s.streamID)
		}
	}
	return preverror
}

func (s *stream) Close() error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	return s.closeWithErrorLocked(ECLOSED)
}

func (s *stream) CloseWrite() error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	preverror := s.writeerror
	if preverror == nil {
		s.writeerror = ECLOSED
		if s.writeleft == 0 {
			s.maywrite.Broadcast()
		}
		s.flags |= flagStreamNeedEOF
		s.owner.addOutgoingDataLocked(s.streamID)
	}
	return preverror
}

func (s *stream) CloseWithError(err error) error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	return s.closeWithErrorLocked(err)
}

func (s *stream) Flush() error {
	s.owner.lock.Lock()
	defer s.owner.lock.Unlock()
	for s.writebuf.len() > 0 {
		s.flushed.Wait()
	}
	return s.writeerror
}
