package copper

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// Various protocol constants
const (
	MaxStreamID                 = 1<<31 - 1
	MinWindowSize               = 1024
	MaxWindowSize               = 1<<31 - 1
	MaxFramePayloadSize         = 0xffffff
	MaxDataFramePayloadSize     = MaxFramePayloadSize
	MaxResetFrameMessageSize    = MaxFramePayloadSize - 4
	MaxSettingsFramePayloadSize = 6 * 1024
	InitialStreamWindow         = 65536
	InitialConnectionWindow     = 1 << 20
	InitialInactivityTimeout    = 60 * time.Second
)

// SettingID identifies a setting
type SettingID uint16

// Setting is an (ID, Value) pair
type Setting struct {
	ID    SettingID
	Value uint32
}

// Reserved settings
const (
	SettingConnWindow             SettingID = 1
	SettingStreamWindow           SettingID = 2
	SettingInactivityMilliseconds SettingID = 3
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

// FrameHeaderSize is the size of the header
const FrameHeaderSize = 9

// FrameHeader is a decoded frame header
type FrameHeader struct {
	Type     FrameType
	Flags    FrameFlags
	Length   uint32
	StreamID uint32
}

func (h FrameHeader) String() string {
	return fmt.Sprintf("[%s flags=%s length=%d stream=%d]", h.Type, h.Flags.String(h.Type), h.Length, h.StreamID)
}

// Frame is used to identify frame types
type Frame interface {
	writeFrameTo(w *FrameWriter) error
}

// FrameReader reads copper frames
type FrameReader struct {
	io.Reader
	buf     [FrameHeaderSize]byte
	scratch []byte
}

// NewFrameReader returns a new FrameReader
func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{
		Reader: r,
	}
}

var frameParsers = map[FrameType]func(h FrameHeader, payload []byte) (Frame, error){
	FramePing:     parsePingFrame,
	FrameData:     parseDataFrame,
	FrameReset:    parseResetFrame,
	FrameWindow:   parseWindowFrame,
	FrameSettings: parseSettingsFrame,
}

// ReadHeader reads a frame header from the reader
func (r *FrameReader) ReadHeader() (h FrameHeader, err error) {
	_, err = io.ReadFull(r, r.buf[:])
	if err != nil {
		return
	}
	return FrameHeader{
		StreamID: uint32(r.buf[0])<<24 | uint32(r.buf[1])<<16 | uint32(r.buf[2])<<8 | uint32(r.buf[3]),
		Length:   uint32(r.buf[4])<<16 | uint32(r.buf[5])<<8 | uint32(r.buf[6]),
		Flags:    FrameFlags(r.buf[7]),
		Type:     FrameType(r.buf[8]),
	}, nil
}

// ReadFrame reads a frame from the reader
func (r *FrameReader) ReadFrame() (Frame, error) {
	h, err := r.ReadHeader()
	if err != nil {
		return nil, err
	}
	var payload []byte
	if h.Length > 0 {
		if len(r.scratch) < int(h.Length) {
			r.scratch = make([]byte, minpow2(int(h.Length)))
		}
		payload = r.scratch[:int(h.Length)]
		_, err = io.ReadFull(r, payload)
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
	parser := frameParsers[h.Type]
	if parser == nil {
		parser = parseUnknownFrame
	}
	return parser(h, payload)
}

// FrameWriter writes copper frames
type FrameWriter struct {
	io.Writer
	buf [FrameHeaderSize]byte
}

// NewFrameWriter returns a new FrameWriter
func NewFrameWriter(w io.Writer) *FrameWriter {
	return &FrameWriter{
		Writer: w,
	}
}

// WriteHeader writes a frame header to the writer
func (w *FrameWriter) WriteHeader(t FrameType, flags FrameFlags, length uint32, streamID uint32) error {
	w.buf[0] = byte(streamID >> 24)
	w.buf[1] = byte(streamID >> 16)
	w.buf[2] = byte(streamID >> 8)
	w.buf[3] = byte(streamID)
	w.buf[4] = byte(length >> 16)
	w.buf[5] = byte(length >> 8)
	w.buf[6] = byte(length)
	w.buf[7] = byte(flags)
	w.buf[8] = byte(t)
	_, err := w.Write(w.buf[:])
	return err
}

// PingData stores 8 bytes of ping data
type PingData [8]byte

// PingDataInt64 converts int64 value to PingData
func PingDataInt64(value int64) PingData {
	var data PingData
	binary.BigEndian.PutUint64(data[:], uint64(value))
	return data
}

// PingFrame represents a PING frame
type PingFrame struct {
	Flags FrameFlags
	Data  PingData
}

func (f *PingFrame) String() string {
	return fmt.Sprintf("PING[flags:%s value:%x]", f.Flags.String(FramePing), f.Data)
}

func parsePingFrame(h FrameHeader, payload []byte) (Frame, error) {
	if h.StreamID != 0 || len(payload) != 8 {
		return nil, EINVALIDFRAME
	}
	f := &PingFrame{
		Flags: h.Flags,
	}
	copy(f.Data[:], payload)
	return f, nil
}

// WritePing writes a PING frame to the writer
func (w *FrameWriter) WritePing(flags FrameFlags, data PingData) error {
	err := w.WriteHeader(FramePing, flags, 8, 0)
	if err == nil {
		_, err = w.Write(data[0:8])
	}
	return err
}

func (f *PingFrame) writeFrameTo(w *FrameWriter) error {
	return w.WritePing(f.Flags, f.Data)
}

// DataFrame represents a DATA frame
type DataFrame struct {
	StreamID uint32
	Flags    FrameFlags
	Data     []byte
}

func (f *DataFrame) String() string {
	return fmt.Sprintf("DATA[stream:%d flags:%s data:% x]", f.StreamID, f.Flags.String(FrameData), f.Data)
}

func parseDataFrame(h FrameHeader, payload []byte) (Frame, error) {
	if h.StreamID&0x80000000 != 0 {
		return nil, EINVALIDFRAME
	}
	return &DataFrame{
		StreamID: h.StreamID,
		Flags:    h.Flags,
		Data:     payload,
	}, nil
}

// WriteData writes a DATA frame
func (w *FrameWriter) WriteData(streamID uint32, flags FrameFlags, data []byte) error {
	if len(data) > MaxDataFramePayloadSize {
		return EINVALIDFRAME
	}
	err := w.WriteHeader(FrameData, flags, uint32(len(data)), streamID)
	if err == nil && len(data) > 0 {
		_, err = w.Write(data)
	}
	return err
}

func (f *DataFrame) writeFrameTo(w *FrameWriter) error {
	return w.WriteData(f.StreamID, f.Flags, f.Data)
}

// ResetFrame represents a RESET frame
type ResetFrame struct {
	StreamID uint32
	Flags    FrameFlags
	Error    error
}

func (f *ResetFrame) String() string {
	return fmt.Sprintf("RESET[stream:%d flags:%s error:%s]", f.StreamID, f.Flags.String(FrameReset), f.Error)
}

func parseResetFrame(h FrameHeader, payload []byte) (Frame, error) {
	if h.StreamID&0x80000000 != 0 || len(payload) < 4 {
		return nil, EINVALIDFRAME
	}
	code := ErrorCode(binary.BigEndian.Uint32(payload[0:4]))
	var err error
	if len(payload) > 4 {
		err = &copperError{
			error: errors.New(string(payload[4:])),
			code:  code,
		}
	} else {
		err = code
	}
	return &ResetFrame{
		StreamID: h.StreamID,
		Flags:    h.Flags,
		Error:    err,
	}, nil
}

// WriteReset writes a RESET frame
func (w *FrameWriter) WriteReset(streamID uint32, flags FrameFlags, err error) error {
	var code ErrorCode
	var message []byte
	switch e := err.(type) {
	case ErrorCode:
		code = e
	case Error:
		code = e.ErrorCode()
		message = []byte(e.Error())
	default:
		code = EINTERNAL
		message = []byte(e.Error())
	}
	if len(message) > MaxResetFrameMessageSize {
		return EINVALIDFRAME
	}
	err = w.WriteHeader(FrameReset, flags, uint32(4+len(message)), streamID)
	if err == nil {
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[0:4], uint32(code))
		_, err = w.Write(buf[0:4])
		if err == nil && len(message) > 0 {
			_, err = w.Write(message)
		}
	}
	return err
}

func (f *ResetFrame) writeFrameTo(w *FrameWriter) error {
	return w.WriteReset(f.StreamID, f.Flags, f.Error)
}

// WindowFrame represents a WINDOW frame
type WindowFrame struct {
	StreamID  uint32
	Flags     FrameFlags
	Increment uint32
}

func (f *WindowFrame) String() string {
	return fmt.Sprintf("WINDOW[stream:%d flags:%s increment:%d]", f.StreamID, f.Flags.String(FrameWindow), f.Increment)
}

func parseWindowFrame(h FrameHeader, payload []byte) (Frame, error) {
	if h.StreamID&0x80000000 != 0 || len(payload) != 4 {
		return nil, EINVALIDFRAME
	}
	return &WindowFrame{
		StreamID:  h.StreamID,
		Flags:     h.Flags,
		Increment: binary.BigEndian.Uint32(payload[0:4]),
	}, nil
}

// WriteWindow writes a WINDOW frame
func (w *FrameWriter) WriteWindow(streamID uint32, increment uint32) error {
	err := w.WriteHeader(FrameWindow, 0, 4, streamID)
	if err == nil {
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[0:4], increment)
		_, err = w.Write(buf[0:4])
	}
	return err
}

func (f *WindowFrame) writeFrameTo(w *FrameWriter) error {
	err := w.WriteHeader(FrameWindow, f.Flags, 4, f.StreamID)
	if err == nil {
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[0:4], f.Increment)
		_, err = w.Write(buf[0:4])
	}
	return err
}

// SettingsFrame represents a SETTINGS frame
type SettingsFrame struct {
	Flags FrameFlags
	Data  []Setting
}

func (f *SettingsFrame) String() string {
	return fmt.Sprintf("SETTINGS[flags:%s data:%v]", f.Flags.String(FrameSettings), f.Data)
}

// Value returns the setting value and its existance flag, given its id
func (f *SettingsFrame) Value(id SettingID) (value uint32, ok bool) {
	for _, s := range f.Data {
		if s.ID == id {
			return s.Value, ok
		}
	}
	return 0, false
}

func parseSettingsFrame(h FrameHeader, payload []byte) (Frame, error) {
	if h.StreamID != 0 {
		return nil, EINVALIDFRAME
	}
	var data []Setting
	if h.Flags.Has(FlagSettingsAck) {
		if len(payload) != 0 {
			return nil, EINVALIDFRAME
		}
	} else {
		if len(payload) > MaxSettingsFramePayloadSize || len(payload)%6 != 0 {
			return nil, EINVALIDFRAME
		}
		count := int(len(payload) / 6)
		data = make([]Setting, 0, count)
		for len(payload) > 0 {
			id := SettingID(binary.BigEndian.Uint16(payload[0:2]))
			value := binary.BigEndian.Uint32(payload[2:6])
			payload = payload[6:]
			data = append(data, Setting{id, value})
		}
	}
	return &SettingsFrame{
		Flags: h.Flags,
		Data:  data,
	}, nil
}

// WriteSettings writes a SETTINGS frame
func (w *FrameWriter) WriteSettings(settings ...Setting) error {
	size := len(settings) * 6
	if size > MaxSettingsFramePayloadSize {
		return EINVALIDFRAME
	}
	err := w.WriteHeader(FrameSettings, 0, uint32(size), 0)
	if err == nil {
		var buf [6]byte
		for _, s := range settings {
			binary.BigEndian.PutUint16(buf[0:2], uint16(s.ID))
			binary.BigEndian.PutUint32(buf[2:6], s.Value)
			_, err = w.Write(buf[0:6])
			if err != nil {
				break
			}
		}
	}
	return err
}

// WriteSettingsAck writes a SETTINGS ACK frame
func (w *FrameWriter) WriteSettingsAck() error {
	return w.WriteHeader(FrameSettings, FlagSettingsAck, 0, 0)
}

func (f *SettingsFrame) writeFrameTo(w *FrameWriter) error {
	if f.Flags.Has(FlagSettingsAck) {
		return w.WriteSettingsAck()
	}
	return w.WriteSettings(f.Data...)
}

// UnknownFrame represents unknown frames
type UnknownFrame struct {
	Type     FrameType
	Flags    FrameFlags
	StreamID uint32
	Payload  []byte
}

func (f *UnknownFrame) String() string {
	return fmt.Sprintf("[%s flags=0x%02x stream=0x%08x, payload=% x]", f.Type, f.Flags.String(f.Type), f.StreamID, f.Payload)
}

func parseUnknownFrame(h FrameHeader, payload []byte) (Frame, error) {
	return &UnknownFrame{
		Type:     h.Type,
		Flags:    h.Flags,
		StreamID: h.StreamID,
		Payload:  payload,
	}, nil
}

func (f *UnknownFrame) writeFrameTo(w *FrameWriter) error {
	if len(f.Payload) > MaxFramePayloadSize {
		return EINVALIDFRAME
	}
	err := w.WriteHeader(f.Type, f.Flags, uint32(len(f.Payload)), f.StreamID)
	if err == nil && len(f.Payload) > 0 {
		_, err = w.Write(f.Payload)
	}
	return err
}
