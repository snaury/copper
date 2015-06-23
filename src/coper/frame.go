package coper

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	settingsFrameID uint32 = 0x80000000 + iota
	windowFrameID
	openFrameID
	resetFrameID
	pingFrameID
	fatalFrameID
)

const (
	maxFramePayloadSize = 0xffffff
)

// Returned when an unknown packet is encountered on the wire
var ErrUnknownFrame = errors.New("unknown frame")

// Returned when an invalid data is encountered in a well defined frame
var ErrInvalidFrame = errors.New("invalid frame")

type frame interface {
	writeFrameTo(w io.Writer) error
}

type dataFrame struct {
	flags    uint8
	streamID int
	data     []byte
}

func (p dataFrame) writeFrameTo(w io.Writer) (err error) {
	if len(p.data) > maxFramePayloadSize {
		return ErrInvalidFrame
	}
	err = writeFrameHeader(w, frameHeader{
		streamID:  uint32(p.streamID),
		flagsSize: uint32(p.flags)<<24 | uint32(len(p.data)),
	})
	if err == nil {
		if len(p.data) > 0 {
			_, err = w.Write(p.data)
		}
	}
	return
}

type settingsFrame struct {
	values map[int]int
}

func (p settingsFrame) writeFrameTo(w io.Writer) (err error) {
	if len(p.values) > 256 {
		return ErrInvalidFrame
	}
	size := 4 + len(p.values)*8
	if size > maxFramePayloadSize {
		return ErrInvalidFrame
	}
	err = writeFrameHeader(w, frameHeader{
		streamID:  settingsFrameID,
		flagsSize: uint32(size),
	})
	if err != nil {
		return
	}
	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(p.values)))
	_, err = w.Write(buf[0:4])
	if err != nil {
		return
	}
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

type windowFrame struct {
	streamID  int
	increment int
}

func (p windowFrame) writeFrameTo(w io.Writer) (err error) {
	err = writeFrameHeader(w, frameHeader{
		streamID:  windowFrameID,
		flagsSize: uint32(8),
	})
	if err == nil {
		var buf [8]byte
		binary.LittleEndian.PutUint32(buf[0:4], uint32(p.streamID))
		binary.LittleEndian.PutUint32(buf[4:8], uint32(p.increment))
		_, err = w.Write(buf[0:8])
	}
	return
}

type openFrame struct {
	flags    uint8
	streamID int
	targetID int64
	data     []byte
}

func (p openFrame) writeFrameTo(w io.Writer) (err error) {
	if len(p.data) > maxFramePayloadSize-12 {
		return ErrInvalidFrame
	}
	err = writeFrameHeader(w, frameHeader{
		streamID:  openFrameID,
		flagsSize: uint32(p.flags)<<24 | uint32(len(p.data)+12),
	})
	if err == nil {
		var buf [12]byte
		binary.LittleEndian.PutUint32(buf[0:4], uint32(p.streamID))
		binary.LittleEndian.PutUint64(buf[4:12], uint64(p.targetID))
		_, err = w.Write(buf[0:12])
		if err == nil {
			if len(p.data) > 0 {
				_, err = w.Write(p.data)
			}
		}
	}
	return
}

type resetFrame struct {
	streamID int
	reason   int
	message  []byte
}

func (p resetFrame) writeFrameTo(w io.Writer) (err error) {
	if len(p.message) > maxFramePayloadSize-8 {
		return ErrInvalidFrame
	}
	err = writeFrameHeader(w, frameHeader{
		streamID:  resetFrameID,
		flagsSize: uint32(len(p.message) + 8),
	})
	if err == nil {
		var buf [8]byte
		binary.LittleEndian.PutUint32(buf[0:4], uint32(p.streamID))
		binary.LittleEndian.PutUint32(buf[4:8], uint32(p.reason))
		_, err = w.Write(buf[0:8])
		if err == nil {
			if len(p.message) > 0 {
				_, err = w.Write(p.message)
			}
		}
	}
	return
}

type pingFrame struct {
	flags uint8
	value uint64
}

func (p pingFrame) writeFrameTo(w io.Writer) (err error) {
	err = writeFrameHeader(w, frameHeader{
		streamID:  pingFrameID,
		flagsSize: uint32(p.flags)<<24 | 8,
	})
	if err == nil {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[0:8], p.value)
		_, err = w.Write(buf[0:8])
	}
	return
}

type fatalFrame struct {
	reason  int
	message []byte
}

func (p fatalFrame) writeFrameTo(w io.Writer) (err error) {
	if len(p.message) > maxFramePayloadSize-4 {
		return ErrInvalidFrame
	}
	err = writeFrameHeader(w, frameHeader{
		streamID:  fatalFrameID,
		flagsSize: uint32(len(p.message) + 4),
	})
	if err == nil {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[0:4], uint32(p.reason))
		_, err = w.Write(buf[0:4])
		if err == nil {
			if len(p.message) > 0 {
				_, err = w.Write(p.message)
			}
		}
	}
	return
}

func readFrame(r io.Reader) (p frame, err error) {
	hdr, err := readFrameHeader(r)
	if err != nil {
		return nil, err
	}
	if hdr.IsDataFrame() {
		var data []byte
		if hdr.Size() > 0 {
			data = make([]byte, hdr.Size())
			_, err = io.ReadFull(r, data)
			if err != nil {
				return nil, err
			}
		}
		return dataFrame{
			streamID: int(hdr.streamID),
			flags:    hdr.Flags(),
			data:     data,
		}, nil
	}
	lr := &io.LimitedReader{
		R: r,
		N: int64(hdr.Size()),
	}
	switch hdr.streamID {
	case settingsFrameID:
		if lr.N < 4 {
			return nil, ErrInvalidFrame
		}
		var buf [8]byte
		_, err = io.ReadFull(lr, buf[0:4])
		if err != nil {
			return
		}
		count := binary.LittleEndian.Uint32(buf[0:4])
		if count > 256 {
			return nil, ErrInvalidFrame
		}
		if lr.N != int64(count*8) {
			return nil, ErrInvalidFrame
		}
		values := make(map[int]int, count)
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
		return settingsFrame{
			values: values,
		}, nil
	case windowFrameID:
		if lr.N != 8 {
			return nil, ErrInvalidFrame
		}
		var buf [8]byte
		_, err = io.ReadFull(lr, buf[0:8])
		if err != nil {
			return
		}
		return windowFrame{
			streamID:  int(binary.LittleEndian.Uint32(buf[0:4])),
			increment: int(binary.LittleEndian.Uint32(buf[4:8])),
		}, nil
	case openFrameID:
		if lr.N < 12 {
			return nil, ErrInvalidFrame
		}
		var buf [12]byte
		_, err = io.ReadFull(lr, buf[0:12])
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
			streamID: int(binary.LittleEndian.Uint32(buf[0:4])),
			targetID: int64(binary.LittleEndian.Uint64(buf[4:12])),
			flags:    hdr.Flags(),
			data:     data,
		}, nil
	case resetFrameID:
		if lr.N < 8 {
			return nil, ErrInvalidFrame
		}
		var buf [8]byte
		_, err = io.ReadFull(lr, buf[0:8])
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
			streamID: int(binary.LittleEndian.Uint32(buf[0:4])),
			reason:   int(binary.LittleEndian.Uint32(buf[4:8])),
			message:  message,
		}, nil
	case pingFrameID:
		if lr.N != 8 {
			return nil, ErrInvalidFrame
		}
		var buf [8]byte
		_, err = io.ReadFull(lr, buf[0:8])
		if err != nil {
			return
		}
		return pingFrame{
			flags: hdr.Flags(),
			value: binary.LittleEndian.Uint64(buf[0:8]),
		}, nil
	case fatalFrameID:
		if lr.N < 4 {
			return nil, ErrInvalidFrame
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
		return fatalFrame{
			reason:  int(binary.LittleEndian.Uint32(buf[0:4])),
			message: message,
		}, nil
	default:
		return nil, ErrUnknownFrame
	}
}
