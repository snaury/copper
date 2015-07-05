package copper

import (
	"github.com/snaury/copper/raw"
)

type rpcError struct {
	error
	code raw.ErrorCode
}

var _ raw.Error = rpcError{}

func (e rpcError) ErrorCode() raw.ErrorCode {
	return e.code
}
