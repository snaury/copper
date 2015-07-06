package copper

import (
	"net"
	"os"
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

func isOverCapacity(err error) bool {
	if e, ok := err.(Error); ok {
		return e.ErrorCode() == EOVERCAPACITY
	}
	return false
}

func toHandlerFunc(handler func(stream Stream)) Handler {
	if handler != nil {
		return HandlerFunc(handler)
	}
	return nil
}

func fullHostname() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}
	cname, err := net.LookupCNAME(hostname)
	if err == nil {
		for len(cname) > 0 && cname[len(cname)-1] == '.' {
			cname = cname[:len(cname)-1]
		}
		if len(cname) > 0 {
			hostname = cname
		}
	}
	return hostname, nil
}
