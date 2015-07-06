package copper

import (
	"encoding/binary"
	"io"
)

type frameHeader struct {
	streamID  uint32
	flagsSize uint32
	frameType uint8
}

func readFrameHeader(r io.Reader) (hdr frameHeader, err error) {
	var buf [9]byte
	_, err = io.ReadFull(r, buf[0:9])
	if err == nil {
		hdr.streamID = binary.LittleEndian.Uint32(buf[0:4])
		hdr.flagsSize = binary.LittleEndian.Uint32(buf[4:8])
		hdr.frameType = buf[8]
	}
	return
}

func writeFrameHeader(w io.Writer, hdr frameHeader) (err error) {
	var buf [9]byte
	binary.LittleEndian.PutUint32(buf[0:4], hdr.streamID)
	binary.LittleEndian.PutUint32(buf[4:8], hdr.flagsSize)
	buf[8] = hdr.frameType
	_, err = w.Write(buf[0:9])
	return
}

func (hdr frameHeader) Flags() uint8 {
	return uint8(hdr.flagsSize >> 24)
}

func (hdr frameHeader) Size() int {
	return int(hdr.flagsSize & 0xffffff)
}
