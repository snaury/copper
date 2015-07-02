package copperd

import (
	"errors"

	"github.com/snaury/copper"
)

const (
	// ESHUTDOWN is used for errors caused by server shutdown
	ESHUTDOWN copper.ErrorCode = 101
	// EOVERCAPACITY is used for errors caused by requests over capacity
	EOVERCAPACITY copper.ErrorCode = 102
)

// ErrShutdown is returned when server is shutting down
var ErrShutdown error = rpcError{
	error: errors.New("server shutdown"),
	code:  ESHUTDOWN,
}

// ErrOverCapacity is returned when service is over its capacity
var ErrOverCapacity error = rpcError{
	error: errors.New("service is over capacity"),
	code:  EOVERCAPACITY,
}

// ErrUnsupported is returned when feature is not supported
var ErrUnsupported error = rpcError{
	error: errors.New("feature is not supported"),
	code:  copper.EUNSUPPORTED,
}
