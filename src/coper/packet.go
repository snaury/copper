package coper

import (
	"encoding/binary"
	"errors"
	"io"
)

// Returned when an unknown packet is encountered on the wire
var ErrUnknownPacket = errors.New("unknown packet")

// Returned when an invalid data is encountered in a well defined packet
var ErrInvalidPacket = errors.New("invalid packet")

type dataPacket struct {
	streamID uint32
	data     []byte
	flags    uint8
}

type settingsPacket struct {
	values map[uint32][]byte
}

type windowPacket struct {
	streamID  uint32
	increment uint32
}

type openPacket struct {
	streamID uint32
	targetID uint32
	data     []byte
	flags    uint8
}

type resetPacket struct {
	streamID uint32
	reason   uint32
	message  []byte
}

type pingPacket struct {
	value uint32
}

type fatalPacket struct {
	reason  uint32
	message []byte
}

func readRawPacket(r io.Reader) (hdr packetHeader, data []byte, err error) {
	hdr, err = readPacketHeader(r)
	if err == nil {
		data = make([]byte, hdr.Size())
		_, err = io.ReadFull(r, data)
	}
	return
}

func readPacket(r io.Reader) (packet interface{}, err error) {
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
	switch hdr.CommandID() {
	case 0:
		if len(data) < 4 {
			return nil, ErrInvalidPacket
		}
		pos := 4
		count := binary.LittleEndian.Uint32(data[0:4])
		if count > 256 {
			return nil, ErrInvalidPacket
		}
		values := make(map[uint32][]byte, count)
		for count > 0 {
			if pos > len(data) || len(data)-pos < 8 {
				return nil, ErrInvalidPacket
			}
			id := binary.LittleEndian.Uint32(data[pos : pos+4])
			pos += 4
			size := binary.LittleEndian.Uint32(data[pos : pos+4])
			pos += 4
			values[id] = data[pos : pos+int(size)]
			pos += int(size)
			count--
		}
		return settingsPacket{
			values: values,
		}, nil
	case 1:
		if len(data) < 8 {
			return nil, ErrInvalidPacket
		}
		return windowPacket{
			streamID:  binary.LittleEndian.Uint32(data[0:4]),
			increment: binary.LittleEndian.Uint32(data[4:8]),
		}, nil
	case 2:
		if len(data) < 8 {
			return nil, ErrInvalidPacket
		}
		return openPacket{
			streamID: binary.LittleEndian.Uint32(data[0:4]),
			targetID: binary.LittleEndian.Uint32(data[4:8]),
			flags:    hdr.Flags(),
			data:     data[8:],
		}, nil
	case 3:
		if len(data) < 8 {
			return nil, ErrInvalidPacket
		}
		return resetPacket{
			streamID: binary.LittleEndian.Uint32(data[0:4]),
			reason:   binary.LittleEndian.Uint32(data[4:8]),
			message:  data[8:],
		}, nil
	case 4:
		if len(data) < 4 {
			return nil, ErrInvalidPacket
		}
		return pingPacket{
			value: binary.LittleEndian.Uint32(data[0:4]),
		}, nil
	case 5:
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

func writePacket(w io.Writer, packet interface{}) (err error) {
	switch v := packet.(type) {
	case dataPacket:
		err = writePacketHeader(w, packetHeader{
			commandAndID: v.streamID,
			flagsAndSize: uint32(v.flags)<<24 | uint32(len(v.data)),
		})
		if err == nil {
			_, err = w.Write(v.data)
		}
	case settingsPacket:
		size := 4 + len(v.values)*8
		for _, data := range v.values {
			size += len(data)
		}
		err = writePacketHeader(w, packetHeader{
			commandAndID: 0x80000000,
			flagsAndSize: uint32(size),
		})
		if err != nil {
			return
		}
		var buf [8]byte
		binary.LittleEndian.PutUint32(buf[0:4], uint32(len(v.values)))
		_, err = w.Write(buf[0:4])
		if err != nil {
			return
		}
		for id, data := range v.values {
			binary.LittleEndian.PutUint32(buf[0:4], id)
			binary.LittleEndian.PutUint32(buf[4:8], uint32(len(data)))
			_, err = w.Write(buf[:])
			if err != nil {
				return
			}
			if len(data) > 0 {
				_, err = w.Write(data)
				if err != nil {
					return
				}
			}
		}
	case windowPacket:
		err = writePacketHeader(w, packetHeader{
			commandAndID: 0x80000001,
			flagsAndSize: 8,
		})
		if err == nil {
			var buf [8]byte
			binary.LittleEndian.PutUint32(buf[0:4], v.streamID)
			binary.LittleEndian.PutUint32(buf[4:8], v.increment)
			_, err = w.Write(buf[:])
		}
	case openPacket:
		err = writePacketHeader(w, packetHeader{
			commandAndID: 0x80000002,
			flagsAndSize: uint32(v.flags)<<24 | uint32(len(v.data)+8),
		})
		if err == nil {
			var buf [8]byte
			binary.LittleEndian.PutUint32(buf[0:4], v.streamID)
			binary.LittleEndian.PutUint32(buf[4:8], v.targetID)
			_, err = w.Write(buf[:])
			if err == nil {
				if len(v.data) > 0 {
					_, err = w.Write(v.data)
				}
			}
		}
	case resetPacket:
		err = writePacketHeader(w, packetHeader{
			commandAndID: 0x80000003,
			flagsAndSize: uint32(len(v.message) + 8),
		})
		if err == nil {
			var buf [8]byte
			binary.LittleEndian.PutUint32(buf[0:4], v.streamID)
			binary.LittleEndian.PutUint32(buf[4:8], v.reason)
			_, err = w.Write(buf[:])
			if err == nil {
				if len(v.message) > 0 {
					_, err = w.Write(v.message)
				}
			}
		}
	case pingPacket:
		err = writePacketHeader(w, packetHeader{
			commandAndID: 0x80000004,
			flagsAndSize: 4,
		})
		if err == nil {
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[0:4], v.value)
			_, err = w.Write(buf[:])
		}
	case fatalPacket:
		err = writePacketHeader(w, packetHeader{
			commandAndID: 0x80000005,
			flagsAndSize: uint32(len(v.message) + 4),
		})
		if err == nil {
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[0:4], v.reason)
			_, err = w.Write(buf[:])
			if err == nil {
				if len(v.message) > 0 {
					_, err = w.Write(v.message)
				}
			}
		}
	default:
		return ErrUnknownPacket
	}
	return
}
