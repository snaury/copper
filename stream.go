package copper

import (
	"io"
	"net"
	"time"
)

// Stream represents a multiplexed copper stream
type Stream interface {
	// Peek is similar to Read, but returns available data without consuming
	// it. The Returned buffer is only valid until the next Peek, Read or
	// Discard call. When returned buffer constitutes all available data on
	// the stream the error (e.g. io.EOF) is also returned.
	Peek() (b []byte, err error)

	// Discard throws away up to n bytes of data, as if consumed by Read, and
	// returns the number of bytes that have been discarded. This call does not
	// block and returns 0 if there is no data in the buffer.
	Discard(n int) int

	// Read reads data from the stream
	Read(b []byte) (n int, err error)

	// ReadByte reads a single byte from the stream
	ReadByte() (c byte, err error)

	// Write writes data to the stream
	Write(b []byte) (n int, err error)

	// WriteByte writes a single byte to the stream
	WriteByte(c byte) error

	// Flush returns when all data has been flushed
	Flush() error

	// Closed returns a channel that's closed when Close is called.
	Closed() <-chan struct{}

	// ReadErr returns an error that caused the read side to be closed.
	ReadErr() error

	// ReadClosed returns a channel that's closed when the read side is closed
	// locally or the write side is closed remotely. In the latter case it may
	// still be possible to read data left in the buffer.
	ReadClosed() <-chan struct{}

	// WriteErr returns an error that caused the write side to be closed.
	WriteErr() error

	// WriteClosed returns a channel that's closed when the read side is closed
	// remotely or the write side is closed locally. In either case no new data
	// may be written to the stream, but in the latter case written data may
	// still be delivered to the remote side.
	WriteClosed() <-chan struct{}

	// Acknowledge sends a signal to the remote that sending data on this
	// stream is desired. This is used in cases when the remote is not willing
	// to send too much data unless it's sure the other side is willing to
	// process it.
	Acknowledge() error

	// Acknowledged returns a channel that's closed when stream has been either
	// acknowledged or closed by the remote.
	Acknowledged() <-chan struct{}

	// IsAcknowledged returns true if the stream has been acknowledged.
	IsAcknowledged() bool

	// Closes closes the stream, discarding any data
	Close() error

	// CloseRead closes the read side of the connection
	CloseRead() error

	// CloseReadError closes the read side with the specifed error
	CloseReadError(err error) error

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

	// LocalAddr returns the local network address
	LocalAddr() net.Addr

	// RemoteAddr returns the remote network address
	RemoteAddr() net.Addr
}

var _ net.Conn = Stream(nil)
var _ io.ByteReader = Stream(nil)
var _ io.ByteWriter = Stream(nil)
