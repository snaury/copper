package copperd

import (
	"errors"

	"github.com/snaury/copper"
)

const (
	// ESHUTDOWN is used for errors caused by server shutdown
	ESHUTDOWN copper.ErrorCode = 101
)

// ErrShutdown is returned when server is shutting down
var ErrShutdown = rpcError{
	error: errors.New("server shutdown"),
	code:  ESHUTDOWN,
}

// ErrUnsupported is returned when feature is not supported
var ErrUnsupported = rpcError{
	error: errors.New("feature is not supported"),
	code:  copper.EUNSUPPORTED,
}
