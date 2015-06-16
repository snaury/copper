package coper

import (
	"encoding/binary"
	"io"
)

type packetHeader struct {
	packetTypeID uint32
	flagsAndSize uint32
}

func readPacketHeader(r io.Reader) (hdr packetHeader, err error) {
	var buf [8]byte
	_, err = io.ReadFull(r, buf[:])
	if err == nil {
		hdr.packetTypeID = binary.LittleEndian.Uint32(buf[0:4])
		hdr.flagsAndSize = binary.LittleEndian.Uint32(buf[4:8])
	}
	return
}

func writePacketHeader(w io.Writer, hdr packetHeader) (err error) {
	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[0:4], hdr.packetTypeID)
	binary.LittleEndian.PutUint32(buf[4:8], hdr.flagsAndSize)
	_, err = w.Write(buf[:])
	return
}

func (hdr packetHeader) IsDataPacket() bool {
	return (hdr.packetTypeID & 0x80000000) == 0
}

func (hdr packetHeader) StreamID() uint32 {
	return hdr.packetTypeID
}

func (hdr packetHeader) Flags() uint8 {
	return byte(hdr.flagsAndSize >> 24)
}

func (hdr packetHeader) Size() uint32 {
	return hdr.flagsAndSize & 0xffffff
}
