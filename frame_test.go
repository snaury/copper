package copper

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"testing"
)

var decodedFrames = []frame{
	&pingFrame{
		flags: 0,
		value: 0x1122334455667788,
	},
	&pingFrame{
		flags: flagAck,
		value: 0x1122334455667788,
	},
	&openFrame{
		streamID: 0x42,
		flags:    0x25,
		targetID: 0x1122334455667788,
		data:     []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8},
	},
	&dataFrame{
		streamID: 0x42,
		flags:    0x25,
		data:     []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8},
	},
	&resetFrame{
		streamID: 0x42,
		flags:    0x25,
		code:     0x55,
		message:  nil,
	},
	&resetFrame{
		streamID: 0x42,
		flags:    0x25,
		code:     0x55,
		message:  []byte{'t', 'e', 's', 't'},
	},
	&windowFrame{
		streamID:  0x42,
		flags:     0x25,
		increment: 0x11223344,
	},
	&settingsFrame{
		flags:  0,
		values: map[int]int{2: 3},
	},
	&settingsFrame{
		flags:  flagAck,
		values: nil,
	},
	&resetFrame{
		code: EUNKNOWN,
	},
}

var printedFrames = []string{
	`PING[flags:0x00 value:1234605616436508552]`,
	`PING[flags:0x01(ACK) value:1234605616436508552]`,
	`OPEN[stream:66 flags:0x25(FIN) target:1234605616436508552 data:ff fe fd fc fb fa f9 f8]`,
	`DATA[stream:66 flags:0x25(FIN) data:ff fe fd fc fb fa f9 f8]`,
	`RESET[stream:66 flags:0x25(FIN) code:ERROR_85 message:""]`,
	`RESET[stream:66 flags:0x25(FIN) code:ERROR_85 message:"test"]`,
	`WINDOW[stream:66 flags:0x25(ACK) increment:287454020]`,
	`SETTINGS[flags:0x00 values:map[2:3]]`,
	`SETTINGS[flags:0x01(ACK) values:map[]]`,
	`RESET[stream:0 flags:0x00 code:EUNKNOWN message:""]`,
}

var rawFrameData = []byte{
	// a PING frame
	0x00, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00,
	0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11,
	// a PING ack frame
	0x00, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x01, 0x00,
	0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11,
	// an OPEN frame
	0x42, 0x00, 0x00, 0x00, 0x10, 0x00, 0x00, 0x25, 0x01,
	0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11,
	0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
	// a DATA frame
	0x42, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x25, 0x02,
	0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
	// a RESET frame
	0x42, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x25, 0x03,
	0x55, 0x00, 0x00, 0x00,
	// a RESET frame + message
	0x42, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x25, 0x03,
	0x55, 0x00, 0x00, 0x00,
	't', 'e', 's', 't',
	// a WINDOW frame
	0x42, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x25, 0x04,
	0x44, 0x33, 0x22, 0x11,
	// a SETTINGS frame
	0x00, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x05,
	0x02, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00,
	// a SETTINGS ack frame
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x05,
	// a RESET frame with a EUNKNOWN error
	0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x03,
	0xff, 0xff, 0xff, 0xff,
}

func TestFrameReading(t *testing.T) {
	r := bytes.NewReader(rawFrameData)

	for _, expected := range decodedFrames {
		f, err := readFrame(r, nil)
		if err != nil {
			t.Fatalf("Unexpected error: %v\nExpected: %v", err, expected)
		}
		if !reflect.DeepEqual(f, expected) {
			t.Fatalf("Unexpected frame %#v\nExpected: %#v", f, expected)
		}
	}

	f, err := readFrame(r, nil)
	if err == nil {
		t.Fatalf("Unexpected frame: %#v", f)
	}
	if err != io.EOF {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestFrameWriting(t *testing.T) {
	w := new(bytes.Buffer)

	for _, frame := range decodedFrames {
		err := frame.writeFrameTo(w)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
	}

	buf := w.Bytes()
	if !reflect.DeepEqual(buf, rawFrameData) {
		t.Fatalf("Unexpected frame data:\n% x\nExpected:\n% x", buf, rawFrameData)
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
	r := bytes.NewReader([]byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff,
	})
	f, err := readFrame(r, nil)
	if err == nil {
		t.Fatalf("Unexpected frame: %#v", f)
	}
	if err != EUNKNOWNFRAME {
		t.Fatalf("Got unexpected error: %v", err)
	}
}
