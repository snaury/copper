package copper

import (
	"fmt"
)

// ErrorCode represents a copper error code
type ErrorCode int

const (
	// EOK is returned when operation finishes normally
	EOK ErrorCode = iota
	// EUNKNOWNFRAME is returned when frame type is unknown
	EUNKNOWNFRAME
	// EINVALIDFRAME is returned when frame has invalid data
	EINVALIDFRAME
	// EWINDOWOVERFLOW is returned when receive window overflows
	EWINDOWOVERFLOW
	// ECONNCLOSED is returned when connection is gracefully closed
	ECONNCLOSED
	// ECLOSED is returned when the stream is closed normally
	ECLOSED
	// EINVALID is returned when data is invalid
	EINVALID
	// EINVALIDSTREAM is returned when incoming stream id is invalid
	EINVALIDSTREAM
	// ENOTARGET is returned when target does not exist
	ENOTARGET
	// ENOSTREAM is returned when stream does not exist
	ENOSTREAM
	// ENOROUTE is returned when there's no route to the target
	ENOROUTE
	// EUNKNOWN is used for unknown errors
	EUNKNOWN = -1
)

var errorMessages = map[ErrorCode]string{
	EOK:             "no error",
	EUNKNOWNFRAME:   "unknown frame",
	EINVALIDFRAME:   "invalid frame",
	EWINDOWOVERFLOW: "receive window overflow",
	ECONNCLOSED:     "connection closed",
	ECLOSED:         "stream closed",
	EINVALID:        "invalid data",
	EINVALIDSTREAM:  "invalid stream",
	ENOTARGET:       "no such target",
	ENOSTREAM:       "no such stream",
	ENOROUTE:        "no route to target",
}

func (e ErrorCode) Error() string {
	return errorMessages[e]
}

// Reason returns the error code itself
func (e ErrorCode) Reason() ErrorCode {
	return e
}

// ErrorWithReason is used to detect a copper error codes
type ErrorWithReason interface {
	error
	Reason() ErrorCode
}

type errorWithReason struct {
	error
	reason ErrorCode
}

type unknownFrameError struct {
	streamID uint32
}

func (e unknownFrameError) Error() string {
	return fmt.Sprintf("unknown frame 0x%08x", e.streamID)
}

func (e unknownFrameError) Reason() ErrorCode {
	return EUNKNOWNFRAME
}

func errorToFatalFrame(err error) fatalFrame {
	if e, ok := err.(ErrorCode); ok {
		return fatalFrame{
			reason:  e,
			message: nil,
		}
	}
	if e, ok := err.(ErrorWithReason); ok {
		return fatalFrame{
			reason:  e.Reason(),
			message: []byte(e.Error()),
		}
	}
	return fatalFrame{
		reason:  EUNKNOWN,
		message: []byte(err.Error()),
	}
}

func errorToResetFrame(flags uint8, streamID int, err error) resetFrame {
	if e, ok := err.(ErrorCode); ok {
		return resetFrame{
			flags:    flags,
			streamID: streamID,
			reason:   e,
			message:  nil,
		}
	}
	if e, ok := err.(ErrorWithReason); ok {
		return resetFrame{
			flags:    flags,
			streamID: streamID,
			reason:   e.Reason(),
			message:  []byte(e.Error()),
		}
	}
	return resetFrame{
		flags:    flags,
		streamID: streamID,
		reason:   EUNKNOWN,
		message:  []byte(err.Error()),
	}
}
