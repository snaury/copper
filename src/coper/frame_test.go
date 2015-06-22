package coper

import (
	"bytes"
	"io"
	"reflect"
	"testing"
)

var expectedFrames = []frame{
	dataFrame{
		streamID: 0x42,
		flags:    0x25,
		data:     []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8},
	},
	settingsFrame{
		values: map[int]int{2: 3},
	},
	windowFrame{
		streamID:  0x42,
		increment: 0x11223344,
	},
	openFrame{
		streamID: 0x42,
		flags:    0x25,
		targetID: 0x1122334455667788,
		data:     []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8},
	},
	resetFrame{
		streamID: 0x42,
		reason:   0x55,
		message:  nil,
	},
	resetFrame{
		streamID: 0x42,
		reason:   0x55,
		message:  []byte{'t', 'e', 's', 't'},
	},
	pingFrame{
		value: 0x11223344,
	},
	fatalFrame{
		reason:  0x55,
		message: nil,
	},
	fatalFrame{
		reason:  0x55,
		message: []byte{'t', 'e', 's', 't'},
	},
}

var rawFrameDataWithJunk = []byte{
	// a DATA frame
	0x42, 0x00, 0x00, 0x00,
	0x08, 0x00, 0x00, 0x25,
	0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
	// a SETTINGS frame + junk padding
	0x00, 0x00, 0x00, 0x80,
	0x10, 0x00, 0x00, 0x00,
	0x01, 0x00, 0x00, 0x00,
	0x02, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00,
	0xff, 0xfe, 0xfd, 0xfc,
	// a WINDOW frame + junk padding
	0x01, 0x00, 0x00, 0x80,
	0x10, 0x00, 0x00, 0x00,
	0x42, 0x00, 0x00, 0x00,
	0x44, 0x33, 0x22, 0x11,
	0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
	// an OPEN frame
	0x02, 0x00, 0x00, 0x80,
	0x14, 0x00, 0x00, 0x25,
	0x42, 0x00, 0x00, 0x00,
	0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11,
	0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
	// a RESET frame
	0x03, 0x00, 0x00, 0x80,
	0x08, 0x00, 0x00, 0x00,
	0x42, 0x00, 0x00, 0x00,
	0x55, 0x00, 0x00, 0x00,
	// a RESET frame + message
	0x03, 0x00, 0x00, 0x80,
	0x0c, 0x00, 0x00, 0x00,
	0x42, 0x00, 0x00, 0x00,
	0x55, 0x00, 0x00, 0x00,
	't', 'e', 's', 't',
	// a PING frame + junk padding
	0x04, 0x00, 0x00, 0x80,
	0x08, 0x00, 0x00, 0x00,
	0x44, 0x33, 0x22, 0x11,
	0xff, 0xfe, 0xfd, 0xfc,
	// a FATAL frame
	0x05, 0x00, 0x00, 0x80,
	0x04, 0x00, 0x00, 0x00,
	0x55, 0x00, 0x00, 0x00,
	// a FATAL frame + message
	0x05, 0x00, 0x00, 0x80,
	0x08, 0x00, 0x00, 0x00,
	0x55, 0x00, 0x00, 0x00,
	't', 'e', 's', 't',
}

var rawFrameDataExpected = []byte{
	// a DATA frame
	0x42, 0x00, 0x00, 0x00,
	0x08, 0x00, 0x00, 0x25,
	0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
	// a SETTINGS frame
	0x00, 0x00, 0x00, 0x80,
	0x0c, 0x00, 0x00, 0x00,
	0x01, 0x00, 0x00, 0x00,
	0x02, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00,
	// a WINDOW frame
	0x01, 0x00, 0x00, 0x80,
	0x08, 0x00, 0x00, 0x00,
	0x42, 0x00, 0x00, 0x00,
	0x44, 0x33, 0x22, 0x11,
	// an OPEN frame
	0x02, 0x00, 0x00, 0x80,
	0x14, 0x00, 0x00, 0x25,
	0x42, 0x00, 0x00, 0x00,
	0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11,
	0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
	// a RESET frame
	0x03, 0x00, 0x00, 0x80,
	0x08, 0x00, 0x00, 0x00,
	0x42, 0x00, 0x00, 0x00,
	0x55, 0x00, 0x00, 0x00,
	// a RESET frame + message
	0x03, 0x00, 0x00, 0x80,
	0x0c, 0x00, 0x00, 0x00,
	0x42, 0x00, 0x00, 0x00,
	0x55, 0x00, 0x00, 0x00,
	't', 'e', 's', 't',
	// a PING frame
	0x04, 0x00, 0x00, 0x80,
	0x04, 0x00, 0x00, 0x00,
	0x44, 0x33, 0x22, 0x11,
	// a FATAL frame
	0x05, 0x00, 0x00, 0x80,
	0x04, 0x00, 0x00, 0x00,
	0x55, 0x00, 0x00, 0x00,
	// a FATAL frame + message
	0x05, 0x00, 0x00, 0x80,
	0x08, 0x00, 0x00, 0x00,
	0x55, 0x00, 0x00, 0x00,
	't', 'e', 's', 't',
}

func TestFrameReading(t *testing.T) {
	r := bytes.NewReader(rawFrameDataWithJunk)

	for _, expected := range expectedFrames {
		f, err := readFrame(r)
		if err != nil {
			t.Fatalf("Unexpected error: %#v", err)
		}
		if !reflect.DeepEqual(f, expected) {
			t.Fatalf("Unexpected frame %#v\nExpected: %#v", f, expected)
		}
	}

	f, err := readFrame(r)
	if err == nil {
		t.Fatalf("Unexpected frame: %#v", f)
	}
	if err != io.EOF {
		t.Fatalf("Unexpected error: %#v", err)
	}
}

func TestFrameWriting(t *testing.T) {
	w := new(bytes.Buffer)

	for _, frame := range expectedFrames {
		err := frame.writeFrameTo(w)
		if err != nil {
			t.Fatalf("Unexpected error: %#v", err)
		}
	}

	buf := w.Bytes()
	if !reflect.DeepEqual(buf, rawFrameDataExpected) {
		t.Fatalf("Unexpected frame data:\n%#v\nExpected:\n%#v", buf, rawFrameDataExpected)
	}
}
