package copper

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

type frameID uint8

const (
	pingFrameID     frameID = 0
	openFrameID     frameID = 1
	dataFrameID     frameID = 2
	resetFrameID    frameID = 3
	windowFrameID   frameID = 4
	settingsFrameID frameID = 5
)

type settingID uint32

const (
	settingConnWindow             settingID = 1
	settingStreamWindow           settingID = 2
	settingInactivityMilliseconds settingID = 3
)

const (
	minWindowSize               = 1024
	maxWindowSize               = 1<<31 - 1
	maxFramePayloadSize         = 0xffffff
	maxOpenFramePayloadSize     = maxFramePayloadSize - 8
	maxDataFramePayloadSize     = maxFramePayloadSize
	maxResetFrameMessageSize    = maxFramePayloadSize - 4
	maxSettingsFramePayloadSize = 8 * 256
)

const (
	// Used for OPEN, DATA and RESET
	flagFin uint8 = 1
	// Used for PING, WINDOW and SETTINGS
	flagAck uint8 = 1
	// Used for WINDOW
	flagInc uint8 = 2
)

type frame interface {
	writeFrameTo(w io.Writer) error
}

type pingFrame struct {
	flags uint8
	value int64
}

func (p *pingFrame) String() string {
	flagstring := fmt.Sprintf("0x%02x", p.flags)
	if p.flags&flagAck != 0 {
		flagstring += "(ACK)"
	}
	return fmt.Sprintf("PING[flags:%s value:%d]", flagstring, p.value)
}

func (p *pingFrame) writeFrameTo(w io.Writer) (err error) {
	err = writeFrameHeader(w, frameHeader{
		length: 8,
		flags:  p.flags,
		id:     pingFrameID,
	})
	if err == nil {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[0:8], uint64(p.value))
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

func (p *openFrame) String() string {
	flagstring := fmt.Sprintf("0x%02x", p.flags)
	if p.flags&flagFin != 0 {
		flagstring += "(FIN)"
	}
	return fmt.Sprintf("OPEN[stream:%d flags:%s target:%d data:% x]", p.streamID, flagstring, p.targetID, p.data)
}

func (p *openFrame) writeFrameTo(w io.Writer) (err error) {
	if len(p.data) > maxOpenFramePayloadSize {
		return EINVALIDFRAME
	}
	err = writeFrameHeader(w, frameHeader{
		streamID: p.streamID,
		length:   uint32(len(p.data) + 8),
		flags:    p.flags,
		id:       openFrameID,
	})
	if err == nil {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[0:8], uint64(p.targetID))
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

func (p *dataFrame) String() string {
	flagstring := fmt.Sprintf("0x%02x", p.flags)
	if p.flags&flagFin != 0 {
		flagstring += "(FIN)"
	}
	return fmt.Sprintf("DATA[stream:%d flags:%s data:% x]", p.streamID, flagstring, p.data)
}

func (p *dataFrame) writeFrameTo(w io.Writer) (err error) {
	if len(p.data) > maxDataFramePayloadSize {
		return EINVALIDFRAME
	}
	err = writeFrameHeader(w, frameHeader{
		streamID: p.streamID,
		length:   uint32(len(p.data)),
		flags:    p.flags,
		id:       dataFrameID,
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
	err      error
}

func (p *resetFrame) String() string {
	flagstring := fmt.Sprintf("0x%02x", p.flags)
	if p.flags&flagFin != 0 {
		flagstring += "(FIN)"
	}
	return fmt.Sprintf("RESET[stream:%d flags:%s error:%s]", p.streamID, flagstring, p.err)
}

func (p *resetFrame) writeFrameTo(w io.Writer) (err error) {
	var code ErrorCode
	var message []byte
	switch e := p.err.(type) {
	case ErrorCode:
		code = e
	case Error:
		code = e.ErrorCode()
		message = []byte(e.Error())
	default:
		code = EINTERNAL
		message = []byte(e.Error())
	}
	if len(message) > maxResetFrameMessageSize {
		return EINVALIDFRAME
	}
	err = writeFrameHeader(w, frameHeader{
		streamID: p.streamID,
		length:   uint32(len(message) + 4),
		flags:    p.flags,
		id:       resetFrameID,
	})
	if err == nil {
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[0:4], uint32(code))
		_, err = w.Write(buf[0:4])
		if err == nil {
			if len(message) > 0 {
				_, err = w.Write(message)
			}
		}
	}
	return
}

type windowFrame struct {
	streamID  uint32
	flags     uint8
	increment uint32
}

func (p *windowFrame) String() string {
	flagstring := fmt.Sprintf("0x%02x", p.flags)
	if p.flags&flagAck != 0 {
		if p.flags&flagInc != 0 {
			flagstring += "(ACK|INC)"
		} else {
			flagstring += "(ACK)"
		}
	} else if p.flags&flagInc != 0 {
		flagstring += "(INC)"
	}
	return fmt.Sprintf("WINDOW[stream:%d flags:%s increment:%d]", p.streamID, flagstring, p.increment)
}

func (p *windowFrame) writeFrameTo(w io.Writer) (err error) {
	err = writeFrameHeader(w, frameHeader{
		streamID: p.streamID,
		length:   4,
		flags:    p.flags,
		id:       windowFrameID,
	})
	if err == nil {
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[0:4], p.increment)
		_, err = w.Write(buf[0:4])
	}
	return
}

type settingsFrame struct {
	flags  uint8
	values map[settingID]uint32
}

func (p *settingsFrame) String() string {
	flagstring := fmt.Sprintf("0x%02x", p.flags)
	if p.flags&flagAck != 0 {
		flagstring += "(ACK)"
	}
	return fmt.Sprintf("SETTINGS[flags:%s values:%v]", flagstring, p.values)
}

func (p *settingsFrame) writeFrameTo(w io.Writer) (err error) {
	size := len(p.values) * 8
	if size > maxSettingsFramePayloadSize {
		return EINVALIDFRAME
	}
	err = writeFrameHeader(w, frameHeader{
		length: uint32(size),
		flags:  p.flags,
		id:     settingsFrameID,
	})
	if err != nil {
		return
	}
	var buf [8]byte
	for id, value := range p.values {
		binary.BigEndian.PutUint32(buf[0:4], uint32(id))
		binary.BigEndian.PutUint32(buf[4:8], value)
		_, err = w.Write(buf[0:8])
		if err != nil {
			return
		}
	}
	return
}

func readFrame(r io.Reader, scratch []byte) (p frame, err error) {
	hdr, err := readFrameHeader(r)
	if err != nil {
		return nil, err
	}
	size := hdr.Size()
	switch hdr.id {
	case pingFrameID:
		if hdr.streamID != 0 || size != 8 {
			return nil, EINVALIDFRAME
		}
		var buf [8]byte
		_, err = io.ReadFull(r, buf[0:8])
		if err != nil {
			return
		}
		return &pingFrame{
			flags: hdr.Flags(),
			value: int64(binary.BigEndian.Uint64(buf[0:8])),
		}, nil
	case openFrameID:
		if hdr.streamID&0x80000000 != 0 || size < 8 {
			return nil, EINVALIDFRAME
		}
		var buf [8]byte
		_, err = io.ReadFull(r, buf[0:8])
		if err != nil {
			return
		}
		size -= 8
		var data []byte
		if size > 0 {
			if size > len(scratch) {
				data = make([]byte, size)
			} else {
				data = scratch[:size]
			}
			_, err = io.ReadFull(r, data)
			if err != nil {
				return
			}
		}
		return &openFrame{
			flags:    hdr.Flags(),
			streamID: hdr.streamID,
			targetID: int64(binary.BigEndian.Uint64(buf[0:8])),
			data:     data,
		}, nil
	case dataFrameID:
		if hdr.streamID&0x80000000 != 0 {
			return nil, EINVALIDFRAME
		}
		var data []byte
		if size > 0 {
			if size > len(scratch) {
				data = make([]byte, size)
			} else {
				data = scratch[:size]
			}
			_, err = io.ReadFull(r, data)
			if err != nil {
				return
			}
		}
		return &dataFrame{
			flags:    hdr.Flags(),
			streamID: hdr.streamID,
			data:     data,
		}, nil
	case resetFrameID:
		if hdr.streamID&0x80000000 != 0 || size < 4 {
			return nil, EINVALIDFRAME
		}
		var buf [4]byte
		_, err = io.ReadFull(r, buf[0:4])
		if err != nil {
			return
		}
		size -= 4
		var message []byte
		if size > 0 {
			if size > len(scratch) {
				message = make([]byte, size)
			} else {
				message = scratch[:size]
			}
			_, err = io.ReadFull(r, message)
			if err != nil {
				return
			}
		}
		code := ErrorCode(binary.BigEndian.Uint32(buf[0:4]))
		var err error
		if len(message) > 0 {
			err = &copperError{
				error: errors.New(string(message)),
				code:  code,
			}
		} else {
			err = code
		}
		return &resetFrame{
			flags:    hdr.Flags(),
			streamID: hdr.streamID,
			err:      err,
		}, nil
	case windowFrameID:
		if hdr.streamID&0x80000000 != 0 || size != 4 {
			return nil, EINVALIDFRAME
		}
		var buf [4]byte
		_, err = io.ReadFull(r, buf[0:4])
		if err != nil {
			return
		}
		return &windowFrame{
			flags:     hdr.Flags(),
			streamID:  hdr.streamID,
			increment: binary.BigEndian.Uint32(buf[0:4]),
		}, nil
	case settingsFrameID:
		if hdr.streamID != 0 {
			return nil, EINVALIDFRAME
		}
		var values map[settingID]uint32
		if hdr.Flags()&flagAck != 0 {
			if size != 0 {
				return nil, EINVALIDFRAME
			}
		} else {
			if size > maxSettingsFramePayloadSize || (size%8) != 0 {
				return nil, EINVALIDFRAME
			}
			var buf [8]byte
			count := int(size / 8)
			values = make(map[settingID]uint32, count)
			for count > 0 {
				_, err = io.ReadFull(r, buf[0:8])
				if err != nil {
					return
				}
				id := settingID(binary.BigEndian.Uint32(buf[0:4]))
				value := binary.BigEndian.Uint32(buf[4:8])
				values[id] = value
				count--
			}
		}
		return &settingsFrame{
			flags:  hdr.Flags(),
			values: values,
		}, nil
	default:
		return nil, EUNKNOWNFRAME
	}
}
