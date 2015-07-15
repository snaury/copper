package copper

import (
	"fmt"
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

	// WaitAck returns when all data has been read by the remote side or there
	// is an error. In case of an error it also returns the number of bytes
	// that have not been acknowledged by the remote side.
	WaitAck() (int, error)

	// WaitAckAny returns when any of the last n bytes have been read by the
	// remote side or there is an error. Returns the number of bytes that have
	// not been acknowledged by the remote side and an error if any.
	// The intended use case is remote sensing, e.g. by calling it with the
	// number of bytes returned from Write it would return when any bytes from
	// that write have been read by the remote side.
	WaitAckAny(n int) (int, error)

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

	// TargetID returns the target id
	TargetID() int64

	// LocalAddr returns the local network address
	LocalAddr() net.Addr

	// RemoteAddr returns the remote network address
	RemoteAddr() net.Addr
}

var _ net.Conn = Stream(nil)
var _ io.ByteReader = Stream(nil)
var _ io.ByteWriter = Stream(nil)

// StreamAddr describes endpoint addresses for streams
type StreamAddr struct {
	NetAddr  net.Addr
	StreamID uint32
	TargetID int64
	Outgoing bool
}

// Network returns "copper" as the name of the network
func (addr *StreamAddr) Network() string {
	return "copper"
}

func (addr *StreamAddr) String() string {
	if addr == nil {
		return "<nil>"
	}
	if addr.Outgoing {
		return fmt.Sprintf("[%s:%s;target=%d]", addr.NetAddr.Network(), addr.NetAddr.String(), addr.TargetID)
	}
	return fmt.Sprintf("[%s:%s;stream=%d]", addr.NetAddr.Network(), addr.NetAddr.String(), addr.StreamID)
}
