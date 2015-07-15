package copper

import (
	"io"
)

type frameHeader struct {
	streamID uint32
	length   uint32
	flags    uint8
	id       frameID
}

func readFrameHeader(r io.Reader) (hdr frameHeader, err error) {
	var buf [9]byte
	_, err = io.ReadFull(r, buf[0:9])
	if err == nil {
		hdr.streamID = uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3])
		hdr.length = uint32(buf[4])<<16 | uint32(buf[5])<<8 | uint32(buf[6])
		hdr.flags = buf[7]
		hdr.id = frameID(buf[8])
	}
	return
}

func writeFrameHeader(w io.Writer, hdr frameHeader) (err error) {
	buf := [...]byte{
		byte(hdr.streamID >> 24),
		byte(hdr.streamID >> 16),
		byte(hdr.streamID >> 8),
		byte(hdr.streamID),
		byte(hdr.length >> 16),
		byte(hdr.length >> 8),
		byte(hdr.length),
		hdr.flags,
		byte(hdr.id),
	}
	_, err = w.Write(buf[0:9])
	return
}

func (hdr frameHeader) Flags() uint8 {
	return hdr.flags
}

func (hdr frameHeader) Size() int {
	return int(hdr.length)
}
