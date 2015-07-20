package copper

import (
	"bytes"
	"fmt"
)

// FrameType is an 8-bit frame type code
type FrameType uint8

// Defined frame type codes
const (
	FramePing     FrameType = 0
	FrameData     FrameType = 1
	FrameReset    FrameType = 2
	FrameWindow   FrameType = 3
	FrameSettings FrameType = 4
)

var frameNames = map[FrameType]string{
	FramePing:     "PING",
	FrameData:     "DATA",
	FrameReset:    "RESET",
	FrameWindow:   "WINDOW",
	FrameSettings: "SETTINGS",
}

func (t FrameType) String() string {
	if name, ok := frameNames[t]; ok {
		return name
	}
	return fmt.Sprintf("UNKNOWN_FRAME_%d", uint8(t))
}

// FrameFlags stores 8-bit frame flags
type FrameFlags uint8

// Defined frame flags
const (
	FlagPingAck FrameFlags = 1

	FlagDataEOF  FrameFlags = 1
	FlagDataOpen FrameFlags = 2
	FlagDataAck  FrameFlags = 4

	FlagResetRead  FrameFlags = 1
	FlagResetWrite FrameFlags = 2

	FlagSettingsAck FrameFlags = 1
)

// Has returns true if all flags from v are present in f
func (f FrameFlags) Has(v FrameFlags) bool {
	return (f & v) == v
}

var flagNames = map[FrameType]map[FrameFlags]string{
	FramePing: {
		FlagPingAck: "ACK",
	},
	FrameData: {
		FlagDataEOF:  "EOF",
		FlagDataOpen: "OPEN",
		FlagDataAck:  "ACK",
	},
	FrameReset: {
		FlagResetRead:  "READ",
		FlagResetWrite: "WRITE",
	},
	FrameSettings: {
		FlagSettingsAck: "ACK",
	},
}

func (f FrameFlags) String(t FrameType) string {
	if f == 0 {
		return "0"
	}
	var buf bytes.Buffer
	var unknown FrameFlags
	count := 0
	for flag := FrameFlags(1); flag != 0; flag <<= 1 {
		if f&flag == 0 {
			continue
		}
		name := flagNames[t][flag]
		if len(name) != 0 {
			if count != 0 {
				buf.WriteByte('|')
			}
			count++
			buf.WriteString(name)
		} else {
			unknown |= flag
		}
	}
	if unknown != 0 {
		if count != 0 {
			buf.WriteByte('|')
		}
		fmt.Fprintf(&buf, "0x%02x", uint8(unknown))
	}
	return buf.String()
}

// FrameHeader is a 9 byte frame header
type FrameHeader struct {
	Type     FrameType
	Flags    FrameFlags
	Length   uint32
	StreamID uint32
}

func (h FrameHeader) String() string {
	return fmt.Sprintf("[%s flags=%s length=%d stream=%d]", h.Type, h.Flags.String(h.Type), h.Length, h.StreamID)
}
