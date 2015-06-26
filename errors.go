package copper

import (
	"errors"
	"fmt"
)

// ErrNoFreeStreamID is returned when all possible stream ids have been allocated
var ErrNoFreeStreamID = errors.New("there are no free stream ids available")

// Error is used to detect copper error codes
type Error interface {
	error
	ErrorCode() ErrorCode
}

// ErrorCode represents a copper error code
type ErrorCode int

var _ Error = EOK

const (
	// EOK is returned when operation finishes normally
	EOK ErrorCode = iota
	// EUNKNOWNFRAME is returned when frame type is unknown
	EUNKNOWNFRAME
	// EINVALIDFRAME is returned when frame has invalid data
	EINVALIDFRAME
	// EWINDOWOVERFLOW is returned when receive window overflows
	EWINDOWOVERFLOW
	// ECONNCLOSED is returned when connection is closed normally
	ECONNCLOSED
	// ESTREAMCLOSED is returned when stream is closed normally
	ESTREAMCLOSED
	// EINVALIDDATA is returned when incoming data is invalid
	EINVALIDDATA
	// EINVALIDSTREAM is returned when incoming stream id is invalid
	EINVALIDSTREAM
	// ENOTARGET is returned when target does not exist
	ENOTARGET
	// ENOSTREAM is returned when stream does not exist
	ENOSTREAM
	// ENOROUTE is returned when there's no route to the target
	ENOROUTE
	// ETIMEOUT is returned when operation has timed out
	ETIMEOUT
	// EUNKNOWN is used for unknown errors
	EUNKNOWN = -1
)

var errorMessages = map[ErrorCode]string{
	EOK:             "no error",
	EUNKNOWNFRAME:   "unknown frame",
	EINVALIDFRAME:   "invalid frame",
	EWINDOWOVERFLOW: "receive window overflow",
	ECONNCLOSED:     "connection closed",
	ESTREAMCLOSED:   "stream closed",
	EINVALIDDATA:    "invalid data",
	EINVALIDSTREAM:  "invalid stream",
	ENOTARGET:       "no such target",
	ENOSTREAM:       "no such stream",
	ENOROUTE:        "no route to target",
	ETIMEOUT:        "operation timed out",
}

func (e ErrorCode) Error() string {
	text := errorMessages[e]
	if len(text) == 0 {
		return fmt.Sprintf("copper error %d", e)
	}
	return text
}

// ErrorCode returns the error code itself
func (e ErrorCode) ErrorCode() ErrorCode {
	return e
}

type copperError struct {
	error
	code ErrorCode
}

var _ Error = &copperError{}

func (e *copperError) ErrorCode() ErrorCode { return e.code }

type unknownFrameError struct {
	frameType uint8
}

var _ Error = &unknownFrameError{}

func (e unknownFrameError) Error() string        { return fmt.Sprintf("unknown frame 0x%02x", e.frameType) }
func (e unknownFrameError) ErrorCode() ErrorCode { return EUNKNOWNFRAME }

type timeoutError struct{}

var _ Error = errTimeout
var errTimeout = &timeoutError{}

func (e *timeoutError) Error() string        { return "i/o timeout" }
func (e *timeoutError) Timeout() bool        { return true }
func (e *timeoutError) Temporary() bool      { return true }
func (e *timeoutError) ErrorCode() ErrorCode { return ETIMEOUT }

func errorToResetFrame(flags uint8, streamID uint32, err error) resetFrame {
	if e, ok := err.(ErrorCode); ok {
		return resetFrame{
			flags:    flags,
			streamID: streamID,
			code:     e,
			message:  nil,
		}
	}
	if e, ok := err.(Error); ok {
		return resetFrame{
			flags:    flags,
			streamID: streamID,
			code:     e.ErrorCode(),
			message:  []byte(e.Error()),
		}
	}
	return resetFrame{
		flags:    flags,
		streamID: streamID,
		code:     EUNKNOWN,
		message:  []byte(err.Error()),
	}
}
