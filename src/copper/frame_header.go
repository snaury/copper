package copper

import (
	"encoding/binary"
	"io"
)

type frameHeader struct {
	streamID  uint32
	flagsSize uint32
}

func readFrameHeader(r io.Reader) (hdr frameHeader, err error) {
	var n int
	var buf [8]byte
	n, err = io.ReadFull(r, buf[0:8])
	if err == nil {
		hdr.streamID = binary.LittleEndian.Uint32(buf[0:4])
		hdr.flagsSize = binary.LittleEndian.Uint32(buf[4:8])
	} else if n == 0 && err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	return
}

func writeFrameHeader(w io.Writer, hdr frameHeader) (err error) {
	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[0:4], hdr.streamID)
	binary.LittleEndian.PutUint32(buf[4:8], hdr.flagsSize)
	_, err = w.Write(buf[0:8])
	return
}

func (hdr frameHeader) IsDataFrame() bool {
	return (hdr.streamID & 0x80000000) == 0
}

func (hdr frameHeader) Flags() uint8 {
	return uint8(hdr.flagsSize >> 24)
}

func (hdr frameHeader) Size() int {
	return int(hdr.flagsSize & 0xffffff)
}
