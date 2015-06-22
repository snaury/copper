package coper

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	settingsPacketID uint32 = 0x80000000 + iota
	windowPacketID
	openPacketID
	resetPacketID
	pingPacketID
	fatalPacketID
)

// Returned when an unknown packet is encountered on the wire
var ErrUnknownPacket = errors.New("unknown packet")

// Returned when an invalid data is encountered in a well defined packet
var ErrInvalidPacket = errors.New("invalid packet")

type packet interface {
	writePacketTo(w io.Writer) error
}

func readRawPacket(r io.Reader) (hdr packetHeader, data []byte, err error) {
	hdr, err = readPacketHeader(r)
	if err == nil {
		data = make([]byte, hdr.Size())
		_, err = io.ReadFull(r, data)
	}
	return
}

type dataPacket struct {
	flags    uint8
	streamID uint32
	data     []byte
}

func (p dataPacket) writePacketTo(w io.Writer) (err error) {
	err = writePacketHeader(w, packetHeader{
		packetTypeID: p.streamID,
		flagsAndSize: uint32(p.flags)<<24 | uint32(len(p.data)),
	})
	if err == nil {
		_, err = w.Write(p.data)
	}
	return
}

type settingsPacket struct {
	values map[uint32]uint32
}

func (p settingsPacket) writePacketTo(w io.Writer) (err error) {
	size := 4 + len(p.values)*8
	err = writePacketHeader(w, packetHeader{
		packetTypeID: settingsPacketID,
		flagsAndSize: uint32(size),
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
		binary.LittleEndian.PutUint32(buf[0:4], id)
		binary.LittleEndian.PutUint32(buf[4:8], value)
		_, err = w.Write(buf[:])
		if err != nil {
			return
		}
	}
	return
}

type windowPacket struct {
	streamID  uint32
	increment uint32
}

func (p windowPacket) writePacketTo(w io.Writer) (err error) {
	err = writePacketHeader(w, packetHeader{
		packetTypeID: windowPacketID,
		flagsAndSize: 8,
	})
	if err == nil {
		var buf [8]byte
		binary.LittleEndian.PutUint32(buf[0:4], p.streamID)
		binary.LittleEndian.PutUint32(buf[4:8], p.increment)
		_, err = w.Write(buf[:])
	}
	return
}

type openPacket struct {
	flags    uint8
	streamID uint32
	targetID uint64
	data     []byte
}

func (p openPacket) writePacketTo(w io.Writer) (err error) {
	err = writePacketHeader(w, packetHeader{
		packetTypeID: openPacketID,
		flagsAndSize: uint32(p.flags)<<24 | uint32(len(p.data)+12),
	})
	if err == nil {
		var buf [12]byte
		binary.LittleEndian.PutUint32(buf[0:4], p.streamID)
		binary.LittleEndian.PutUint64(buf[4:12], p.targetID)
		_, err = w.Write(buf[:])
		if err == nil {
			if len(p.data) > 0 {
				_, err = w.Write(p.data)
			}
		}
	}
	return
}

type resetPacket struct {
	streamID uint32
	reason   uint32
	message  []byte
}

func (p resetPacket) writePacketTo(w io.Writer) (err error) {
	err = writePacketHeader(w, packetHeader{
		packetTypeID: resetPacketID,
		flagsAndSize: uint32(len(p.message) + 8),
	})
	if err == nil {
		var buf [8]byte
		binary.LittleEndian.PutUint32(buf[0:4], p.streamID)
		binary.LittleEndian.PutUint32(buf[4:8], p.reason)
		_, err = w.Write(buf[:])
		if err == nil {
			if len(p.message) > 0 {
				_, err = w.Write(p.message)
			}
		}
	}
	return
}

type pingPacket struct {
	value uint32
}

func (p pingPacket) writePacketTo(w io.Writer) (err error) {
	err = writePacketHeader(w, packetHeader{
		packetTypeID: pingPacketID,
		flagsAndSize: 4,
	})
	if err == nil {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[0:4], p.value)
		_, err = w.Write(buf[:])
	}
	return
}

type fatalPacket struct {
	reason  uint32
	message []byte
}

func (p fatalPacket) writePacketTo(w io.Writer) (err error) {
	err = writePacketHeader(w, packetHeader{
		packetTypeID: fatalPacketID,
		flagsAndSize: uint32(len(p.message) + 4),
	})
	if err == nil {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[0:4], p.reason)
		_, err = w.Write(buf[:])
		if err == nil {
			if len(p.message) > 0 {
				_, err = w.Write(p.message)
			}
		}
	}
	return
}

func readPacket(r io.Reader) (p packet, err error) {
	hdr, data, err := readRawPacket(r)
	if err != nil {
		return nil, err
	}
	if hdr.IsDataPacket() {
		return dataPacket{
			streamID: hdr.StreamID(),
			flags:    hdr.Flags(),
			data:     data,
		}, nil
	}
	switch hdr.packetTypeID {
	case settingsPacketID:
		if len(data) < 4 {
			return nil, ErrInvalidPacket
		}
		pos := 4
		count := binary.LittleEndian.Uint32(data[0:4])
		if count > 256 {
			return nil, ErrInvalidPacket
		}
		values := make(map[uint32]uint32, count)
		for count > 0 {
			if pos > len(data) || len(data)-pos < 8 {
				return nil, ErrInvalidPacket
			}
			id := binary.LittleEndian.Uint32(data[pos : pos+4])
			pos += 4
			value := binary.LittleEndian.Uint32(data[pos : pos+4])
			pos += 4
			values[id] = value
			count--
		}
		return settingsPacket{
			values: values,
		}, nil
	case windowPacketID:
		if len(data) < 8 {
			return nil, ErrInvalidPacket
		}
		return windowPacket{
			streamID:  binary.LittleEndian.Uint32(data[0:4]),
			increment: binary.LittleEndian.Uint32(data[4:8]),
		}, nil
	case openPacketID:
		if len(data) < 12 {
			return nil, ErrInvalidPacket
		}
		return openPacket{
			streamID: binary.LittleEndian.Uint32(data[0:4]),
			targetID: binary.LittleEndian.Uint64(data[4:12]),
			flags:    hdr.Flags(),
			data:     data[8:],
		}, nil
	case resetPacketID:
		if len(data) < 8 {
			return nil, ErrInvalidPacket
		}
		return resetPacket{
			streamID: binary.LittleEndian.Uint32(data[0:4]),
			reason:   binary.LittleEndian.Uint32(data[4:8]),
			message:  data[8:],
		}, nil
	case pingPacketID:
		if len(data) < 4 {
			return nil, ErrInvalidPacket
		}
		return pingPacket{
			value: binary.LittleEndian.Uint32(data[0:4]),
		}, nil
	case fatalPacketID:
		if len(data) < 4 {
			return nil, ErrInvalidPacket
		}
		return fatalPacket{
			reason:  binary.LittleEndian.Uint32(data[0:4]),
			message: data[4:],
		}, nil
	default:
		return nil, ErrUnknownPacket
	}
}
