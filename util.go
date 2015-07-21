package copper

import (
	"net"
)

func decrementCounterUint32(counters map[uint32]int, key uint32) bool {
	newValue := counters[key] - 1
	if newValue != 0 {
		counters[key] = newValue
		return false
	}
	delete(counters, key)
	return true
}

func isTimeout(err error) bool {
	if e, ok := err.(net.Error); ok {
		return e.Timeout()
	}
	return false
}

func isCopperError(err error) bool {
	if _, ok := err.(Error); ok {
		return true
	}
	return false
}

func isOverCapacity(err error) bool {
	if e, ok := err.(Error); ok {
		return e.ErrorCode() == EOVERCAPACITY
	}
	return false
}

func toHandlerFunc(handler func(stream Stream) error) Handler {
	if handler != nil {
		return HandlerFunc(handler)
	}
	return nil
}
