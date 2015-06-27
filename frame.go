package copper

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	pingFrameID uint8 = iota
	openFrameID
	dataFrameID
	resetFrameID
	windowFrameID
	settingsFrameID
)

const (
	maxFramePayloadSize         = 0xffffff
	maxOpenFramePayloadSize     = maxFramePayloadSize - 8
	maxResetFrameMessageSize    = maxFramePayloadSize - 4
	maxSettingsFramePayloadSize = 8 * 256
)

const (
	// Used for PING and SETTINGS
	flagAck uint8 = 1
	flagFin uint8 = 1
)

type frame interface {
	writeFrameTo(w io.Writer) error
}

type pingFrame struct {
	flags uint8
	value int64
}

func (p pingFrame) String() string {
	flagstring := fmt.Sprintf("0x%02x", p.flags)
	if p.flags&flagAck != 0 {
		flagstring += "(ACK)"
	}
	return fmt.Sprintf("PING[flags:%s value:%d]", flagstring, p.value)
}

func (p pingFrame) writeFrameTo(w io.Writer) (err error) {
	err = writeFrameHeader(w, frameHeader{
		flagsSize: uint32(p.flags)<<24 | 8,
		frameType: pingFrameID,
	})
	if err == nil {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[0:8], uint64(p.value))
		_, err = w.Write(buf[0:8])
	}
	return
}

type openFrame struct {
	streamID uint32
	flags    uint8
	targetID int64
	data     []byte
}

func (p openFrame) String() string {
	flagstring := fmt.Sprintf("0x%02x", p.flags)
	if p.flags&flagFin != 0 {
		flagstring += "(FIN)"
	}
	return fmt.Sprintf("OPEN[stream:%d flags:%s target:%d data:% x]", p.streamID, flagstring, p.targetID, p.data)
}

func (p openFrame) writeFrameTo(w io.Writer) (err error) {
	if len(p.data) > maxOpenFramePayloadSize {
		return EINVALIDFRAME
	}
	err = writeFrameHeader(w, frameHeader{
		streamID:  p.streamID,
		flagsSize: uint32(p.flags)<<24 | uint32(len(p.data)+8),
		frameType: openFrameID,
	})
	if err == nil {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[0:8], uint64(p.targetID))
		_, err = w.Write(buf[0:8])
		if err == nil {
			if len(p.data) > 0 {
				_, err = w.Write(p.data)
			}
		}
	}
	return
}

type dataFrame struct {
	streamID uint32
	flags    uint8
	data     []byte
}

func (p dataFrame) String() string {
	flagstring := fmt.Sprintf("0x%02x", p.flags)
	if p.flags&flagFin != 0 {
		flagstring += "(FIN)"
	}
	return fmt.Sprintf("DATA[stream:%d flags:%s data:% x]", p.streamID, flagstring, p.data)
}

func (p dataFrame) writeFrameTo(w io.Writer) (err error) {
	if len(p.data) > maxFramePayloadSize {
		return EINVALIDFRAME
	}
	err = writeFrameHeader(w, frameHeader{
		streamID:  p.streamID,
		flagsSize: uint32(p.flags)<<24 | uint32(len(p.data)),
		frameType: dataFrameID,
	})
	if err == nil {
		if len(p.data) > 0 {
			_, err = w.Write(p.data)
		}
	}
	return
}

type resetFrame struct {
	streamID uint32
	flags    uint8
	code     ErrorCode
	message  []byte
}

func (p resetFrame) String() string {
	flagstring := fmt.Sprintf("0x%02x", p.flags)
	if p.flags&flagFin != 0 {
		flagstring += "(FIN)"
	}
	return fmt.Sprintf("RESET[stream:%d flags:%s code:%s message:%q]", p.streamID, flagstring, p.code.String(), p.message)
}

func (p resetFrame) writeFrameTo(w io.Writer) (err error) {
	if len(p.message) > maxResetFrameMessageSize {
		return EINVALIDFRAME
	}
	err = writeFrameHeader(w, frameHeader{
		streamID:  p.streamID,
		flagsSize: uint32(p.flags)<<24 | uint32(len(p.message)+4),
		frameType: resetFrameID,
	})
	if err == nil {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[0:4], uint32(p.code))
		_, err = w.Write(buf[0:4])
		if err == nil {
			if len(p.message) > 0 {
				_, err = w.Write(p.message)
			}
		}
	}
	return
}

func (p resetFrame) toError() error {
	if len(p.message) == 0 {
		return p.code
	}
	return &copperError{
		error: errors.New(string(p.message)),
		code:  p.code,
	}
}

type windowFrame struct {
	streamID  uint32
	flags     uint8
	increment uint32
}

func (p windowFrame) String() string {
	return fmt.Sprintf("WINDOW[stream:%d flags:0x%02x increment:%d]", p.streamID, p.flags, p.increment)
}

func (p windowFrame) writeFrameTo(w io.Writer) (err error) {
	err = writeFrameHeader(w, frameHeader{
		streamID:  p.streamID,
		flagsSize: uint32(p.flags)<<24 | uint32(4),
		frameType: windowFrameID,
	})
	if err == nil {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[0:4], p.increment)
		_, err = w.Write(buf[0:4])
	}
	return
}

type settingsFrame struct {
	flags  uint8
	values map[int]int
}

func (p settingsFrame) String() string {
	flagstring := fmt.Sprintf("0x%02x", p.flags)
	if p.flags&flagAck != 0 {
		flagstring += "(ACK)"
	}
	return fmt.Sprintf("SETTINGS[flags:%s values:%v]", flagstring, p.values)
}

func (p settingsFrame) writeFrameTo(w io.Writer) (err error) {
	size := len(p.values) * 8
	if size > maxSettingsFramePayloadSize {
		return EINVALIDFRAME
	}
	err = writeFrameHeader(w, frameHeader{
		flagsSize: uint32(p.flags)<<24 | uint32(size),
		frameType: settingsFrameID,
	})
	if err != nil {
		return
	}
	var buf [8]byte
	for id, value := range p.values {
		binary.LittleEndian.PutUint32(buf[0:4], uint32(id))
		binary.LittleEndian.PutUint32(buf[4:8], uint32(value))
		_, err = w.Write(buf[0:8])
		if err != nil {
			return
		}
	}
	return
}

func readFrame(r io.Reader) (p frame, err error) {
	hdr, err := readFrameHeader(r)
	if err != nil {
		return nil, err
	}
	lr := &io.LimitedReader{
		R: r,
		N: int64(hdr.Size()),
	}
	switch hdr.frameType {
	case pingFrameID:
		if hdr.streamID != 0 || lr.N != 8 {
			return nil, EINVALIDFRAME
		}
		var buf [8]byte
		_, err = io.ReadFull(lr, buf[0:8])
		if err != nil {
			return
		}
		return pingFrame{
			flags: hdr.Flags(),
			value: int64(binary.LittleEndian.Uint64(buf[0:8])),
		}, nil
	case openFrameID:
		if hdr.streamID&0x80000000 != 0 || lr.N < 8 {
			return nil, EINVALIDFRAME
		}
		var buf [8]byte
		_, err = io.ReadFull(lr, buf[0:8])
		if err != nil {
			return
		}
		var data []byte
		if lr.N > 0 {
			data = make([]byte, int(lr.N))
			_, err = io.ReadFull(lr, data)
			if err != nil {
				return
			}
		}
		return openFrame{
			flags:    hdr.Flags(),
			streamID: hdr.streamID,
			targetID: int64(binary.LittleEndian.Uint64(buf[0:8])),
			data:     data,
		}, nil
	case dataFrameID:
		if hdr.streamID&0x80000000 != 0 {
			return nil, EINVALIDFRAME
		}
		var data []byte
		if lr.N > 0 {
			data = make([]byte, int(lr.N))
			_, err = io.ReadFull(lr, data)
			if err != nil {
				return
			}
		}
		return dataFrame{
			flags:    hdr.Flags(),
			streamID: hdr.streamID,
			data:     data,
		}, nil
	case resetFrameID:
		if hdr.streamID&0x80000000 != 0 || lr.N < 4 {
			return nil, EINVALIDFRAME
		}
		var buf [4]byte
		_, err = io.ReadFull(lr, buf[0:4])
		if err != nil {
			return
		}
		var message []byte
		if lr.N > 0 {
			message = make([]byte, int(lr.N))
			_, err = io.ReadFull(lr, message)
			if err != nil {
				return
			}
		}
		return resetFrame{
			flags:    hdr.Flags(),
			streamID: hdr.streamID,
			code:     ErrorCode(int32(binary.LittleEndian.Uint32(buf[0:4]))),
			message:  message,
		}, nil
	case windowFrameID:
		if hdr.streamID&0x80000000 != 0 || lr.N != 4 {
			return nil, EINVALIDFRAME
		}
		var buf [4]byte
		_, err = io.ReadFull(lr, buf[0:4])
		if err != nil {
			return
		}
		return windowFrame{
			flags:     hdr.Flags(),
			streamID:  hdr.streamID,
			increment: binary.LittleEndian.Uint32(buf[0:4]),
		}, nil
	case settingsFrameID:
		if hdr.streamID != 0 {
			return nil, EINVALIDFRAME
		}
		var values map[int]int
		if hdr.Flags()&flagAck != 0 {
			if lr.N != 0 {
				return nil, EINVALIDFRAME
			}
		} else {
			if lr.N > maxSettingsFramePayloadSize || (lr.N%8) != 0 {
				return nil, EINVALIDFRAME
			}
			var buf [8]byte
			count := int(lr.N / 8)
			values = make(map[int]int, count)
			for count > 0 {
				_, err = io.ReadFull(lr, buf[0:8])
				if err != nil {
					return
				}
				id := int(binary.LittleEndian.Uint32(buf[0:4]))
				value := int(binary.LittleEndian.Uint32(buf[4:8]))
				values[id] = value
				count--
			}
		}
		return settingsFrame{
			flags:  hdr.Flags(),
			values: values,
		}, nil
	default:
		return nil, EUNKNOWNFRAME
	}
}
