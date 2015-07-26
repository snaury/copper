package copper

import (
	"errors"
	"fmt"
)

// ErrNoFreeStreamID is returned when all possible stream ids have been allocated
var ErrNoFreeStreamID = errors.New("there are no free stream ids available")

// errMultipleReads is returned when simultaneous read operations are detected
var errMultipleReads = errors.New("multiple simultaneous reads are not allowed")

// errMultipleWrites is returned when simultaneous write operations are detected
var errMultipleWrites = errors.New("multiple simultaneous writes are not allowed")

// Error is used to detect copper error codes
type Error interface {
	error
	ErrorCode() ErrorCode
}

// ErrorCode represents a copper error code
type ErrorCode int

var _ Error = ErrorCode(0)

const (
	// EINTERNAL is returned when there is an internal error
	EINTERNAL = ErrorCode(1)

	// ECLOSED is returned when stream is closed normally
	ECLOSED = ErrorCode(100)
	// EINVALID is returned when received data is invalid
	EINVALID = ErrorCode(101)
	// ETIMEOUT is returned when operation times out
	ETIMEOUT = ErrorCode(102)
	// ENOROUTE is returned when there's no route to the target
	ENOROUTE = ErrorCode(103)
	// ENOTARGET is returned when target does not exist
	ENOTARGET = ErrorCode(104)
	// EUNSUPPORTED is returned when feature is not supported
	EUNSUPPORTED = ErrorCode(105)
	// EOVERCAPACITY is returned when server is over capacity
	EOVERCAPACITY = ErrorCode(106)

	// ECONNCLOSED is returned when connection is closed normally
	ECONNCLOSED = ErrorCode(200)
	// ECONNSHUTDOWN is returned when endpoint is shutting down
	ECONNSHUTDOWN = ErrorCode(201)
	// EUNKNOWNFRAME is returned when frame type is unknown
	EUNKNOWNFRAME = ErrorCode(202)
	// EINVALIDFRAME is returned when frame has invalid data
	EINVALIDFRAME = ErrorCode(203)
	// EWINDOWOVERFLOW is returned when receive window overflows
	EWINDOWOVERFLOW = ErrorCode(204)
	// EINVALIDSTREAM is returned when incoming stream id is invalid
	EINVALIDSTREAM = ErrorCode(205)
	// EUNKNOWNSTREAM is returned when stream does not exist
	EUNKNOWNSTREAM = ErrorCode(206)
	// ECONNTIMEOUT is returned when connection times out
	ECONNTIMEOUT = ErrorCode(207)
)

var errorMessages = map[ErrorCode]string{
	EINTERNAL: "internal error",

	ECLOSED:       "stream closed",
	EINVALID:      "data is not valid",
	ETIMEOUT:      "operation timed out",
	ENOROUTE:      "no route to target",
	ENOTARGET:     "no such target",
	EUNSUPPORTED:  "feature is not supported",
	EOVERCAPACITY: "server is over capacity",

	ECONNCLOSED:     "connection closed",
	ECONNSHUTDOWN:   "connection is shutting down",
	EUNKNOWNFRAME:   "unknown frame type",
	EINVALIDFRAME:   "invalid frame data",
	EWINDOWOVERFLOW: "receive window overflow",
	EINVALIDSTREAM:  "received invalid stream id",
	EUNKNOWNSTREAM:  "received unknown stream id",
	ECONNTIMEOUT:    "connection timed out",
}

var errorNames = map[ErrorCode]string{
	EINTERNAL: "EINTERNAL",

	ECLOSED:       "ECLOSED",
	EINVALID:      "EINVALID",
	ETIMEOUT:      "ETIMEOUT",
	ENOROUTE:      "ENOROUTE",
	ENOTARGET:     "ENOTARGET",
	EUNSUPPORTED:  "EUNSUPPORTED",
	EOVERCAPACITY: "EOVERCAPACITY",

	ECONNCLOSED:     "ECONNCLOSED",
	ECONNSHUTDOWN:   "ECONNSHUTDOWN",
	EUNKNOWNFRAME:   "EUNKNOWNFRAME",
	EINVALIDFRAME:   "EINVALIDFRAME",
	EWINDOWOVERFLOW: "EWINDOWOVERFLOW",
	EINVALIDSTREAM:  "EINVALIDSTREAM",
	EUNKNOWNSTREAM:  "EUNKNOWNSTREAM",
	ECONNTIMEOUT:    "ECONNTIMEOUT",
}

func (e ErrorCode) Error() string {
	text := errorMessages[e]
	if len(text) == 0 {
		return fmt.Sprintf("copper error %d", e)
	}
	return text
}

func (e ErrorCode) String() string {
	text := errorNames[e]
	if len(text) == 0 {
		return fmt.Sprintf("ERROR_%d", e)
	}
	return text
}

// ErrorCode returns the copper error code
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

var _ Error = &timeoutError{}
var errTimeout error = &timeoutError{}

func (e *timeoutError) Error() string        { return "i/o timeout" }
func (e *timeoutError) Timeout() bool        { return true }
func (e *timeoutError) Temporary() bool      { return true }
func (e *timeoutError) ErrorCode() ErrorCode { return ETIMEOUT }
