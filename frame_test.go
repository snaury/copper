package copper

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
)

var decodedFrames = []Frame{
	&PingFrame{
		Flags: 0,
		Data:  PingDataInt64(0x1122334455667788),
	},
	&PingFrame{
		Flags: FlagPingAck,
		Data:  PingDataInt64(0x1122334455667788),
	},
	&DataFrame{
		StreamID: 0x42,
		Flags:    FlagDataOpen,
		Data:     []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8},
	},
	&DataFrame{
		StreamID: 0x42,
		Flags:    FlagDataEOF,
		Data:     []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8},
	},
	&ResetFrame{
		StreamID: 0x42,
		Flags:    FlagResetRead | FlagResetWrite,
		Error:    EINVALID,
	},
	&ResetFrame{
		StreamID: 0x42,
		Flags:    FlagResetRead | FlagResetWrite,
		Error: &copperError{
			error: errors.New("test"),
			code:  EINVALID,
		},
	},
	&WindowFrame{
		StreamID:  0x42,
		Flags:     0x25,
		Increment: 0x11223344,
	},
	&SettingsFrame{
		Flags: 0,
		Data: []Setting{
			{SettingConnWindow, 7},
			{SettingStreamWindow, 6},
		},
	},
	&SettingsFrame{
		Flags: FlagSettingsAck,
	},
	&ResetFrame{
		Error: EINTERNAL,
	},
}

var printedFrames = []string{
	`PING[flags:0 value:1122334455667788]`,
	`PING[flags:ACK value:1122334455667788]`,
	`DATA[stream:66 flags:OPEN data:ff fe fd fc fb fa f9 f8]`,
	`DATA[stream:66 flags:EOF data:ff fe fd fc fb fa f9 f8]`,
	`RESET[stream:66 flags:READ|WRITE error:data is not valid]`,
	`RESET[stream:66 flags:READ|WRITE error:test]`,
	`WINDOW[stream:66 flags:0x25 increment:287454020]`,
	`SETTINGS[flags:0 data:[{1 7} {2 6}]]`,
	`SETTINGS[flags:ACK data:[]]`,
	`RESET[stream:0 flags:0 error:internal error]`,
}

var rawFrameData = []byte{
	// a PING frame
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00,
	0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
	// a PING ack frame
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x08, 0x01, 0x00,
	0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
	// a DATA frame
	0x00, 0x00, 0x00, 0x42, 0x00, 0x00, 0x08, 0x02, 0x01,
	0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
	// a DATA frame
	0x00, 0x00, 0x00, 0x42, 0x00, 0x00, 0x08, 0x01, 0x01,
	0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
	// a RESET frame
	0x00, 0x00, 0x00, 0x42, 0x00, 0x00, 0x04, 0x03, 0x02,
	0x00, 0x00, 0x00, 0x65,
	// a RESET frame + message
	0x00, 0x00, 0x00, 0x42, 0x00, 0x00, 0x08, 0x03, 0x02,
	0x00, 0x00, 0x00, 0x65,
	't', 'e', 's', 't',
	// a WINDOW frame
	0x00, 0x00, 0x00, 0x42, 0x00, 0x00, 0x04, 0x25, 0x03,
	0x11, 0x22, 0x33, 0x44,
	// a SETTINGS frame
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x0c, 0x00, 0x04,
	0x00, 0x01, 0x00, 0x00, 0x00, 0x07,
	0x00, 0x02, 0x00, 0x00, 0x00, 0x06,
	// a SETTINGS ack frame
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x04,
	// a RESET frame with a EINTERNAL error
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x02,
	0x00, 0x00, 0x00, 0x01,
}

func TestFrameReading(t *testing.T) {
	r := NewFrameReader(bytes.NewReader(rawFrameData))

	for _, expected := range decodedFrames {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("Unexpected error: %v\nExpected: %v", err, expected)
		}
		if !reflect.DeepEqual(f, expected) {
			t.Fatalf("Unexpected frame %#v\nExpected: %#v", f, expected)
		}
	}

	f, err := r.ReadFrame()
	if err == nil {
		t.Fatalf("Unexpected frame: %#v", f)
	}
	if err != io.EOF {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestFrameWriting(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)

	for _, frame := range decodedFrames {
		err := frame.writeFrameTo(w)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
	}

	data := buf.Bytes()
	if !reflect.DeepEqual(data, rawFrameData) {
		t.Fatalf("Unexpected frame data:\n% x\nExpected:\n% x", data, rawFrameData)
	}
}

func TestFramePrinting(t *testing.T) {
	for index, frame := range decodedFrames {
		expected := printedFrames[index]
		printed := fmt.Sprintf("%v", frame)
		if printed != expected {
			t.Errorf("Unexpected result: %s (expected %s)", printed, expected)
		}
	}
}

func TestFrameErrors(t *testing.T) {
	r := NewFrameReader(bytes.NewReader([]byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff,
	}))
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("Got unexpected error: %v", err)
	}
	if _, ok := f.(*UnknownFrame); !ok {
		t.Fatalf("Unexpected frame: %#v", f)
	}
}
