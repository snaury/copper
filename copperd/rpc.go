package copperd

import (
	"github.com/snaury/copper"
)

type rpcError struct {
	error
	code copper.ErrorCode
}

var _ copper.Error = rpcError{}

func (e rpcError) ErrorCode() copper.ErrorCode {
	return e.code
}
