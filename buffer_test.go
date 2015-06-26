package copper

import (
	"reflect"
	"testing"
)

func makebuffer(data []byte, off int, size int) buffer {
	var b buffer
	b.buf = make([]byte, len(data))
	copy(b.buf, data)
	b.off = off
	b.buf = b.buf[:size]
	return b
}

type bufferReadTestCase struct {
	data     []byte
	off      int
	size     int
	readsize int
	expected []byte
	newoff   int
	newsize  int
}

var bufferReadTestCases = []bufferReadTestCase{
	{[]byte{0, 1, 2, 3}, 0, 2, 0, []byte{}, 0, 2},
	{[]byte{0, 1, 2, 3}, 0, 2, 1, []byte{0}, 1, 1},
	{[]byte{0, 1, 2, 3}, 0, 2, 2, []byte{0, 1}, 0, 0},
	{[]byte{0, 1, 2, 3}, 0, 2, 3, []byte{0, 1}, 0, 0},

	{[]byte{0, 1, 2, 3}, 0, 4, 0, []byte{}, 0, 4},
	{[]byte{0, 1, 2, 3}, 0, 4, 1, []byte{0}, 1, 3},
	{[]byte{0, 1, 2, 3}, 0, 4, 2, []byte{0, 1}, 2, 2},
	{[]byte{0, 1, 2, 3}, 0, 4, 3, []byte{0, 1, 2}, 3, 1},
	{[]byte{0, 1, 2, 3}, 0, 4, 4, []byte{0, 1, 2, 3}, 0, 0},
	{[]byte{0, 1, 2, 3}, 0, 4, 5, []byte{0, 1, 2, 3}, 0, 0},

	{[]byte{0, 1, 2, 3}, 2, 2, 0, []byte{}, 2, 2},
	{[]byte{0, 1, 2, 3}, 2, 2, 1, []byte{2}, 3, 1},
	{[]byte{0, 1, 2, 3}, 2, 2, 2, []byte{2, 3}, 0, 0},
	{[]byte{0, 1, 2, 3}, 2, 2, 3, []byte{2, 3}, 0, 0},

	{[]byte{0, 1, 2, 3}, 2, 3, 0, []byte{}, 2, 3},
	{[]byte{0, 1, 2, 3}, 2, 3, 1, []byte{2}, 3, 2},
	{[]byte{0, 1, 2, 3}, 2, 3, 2, []byte{2, 3}, 0, 1},
	{[]byte{0, 1, 2, 3}, 2, 3, 3, []byte{2, 3, 0}, 0, 0},
	{[]byte{0, 1, 2, 3}, 2, 3, 4, []byte{2, 3, 0}, 0, 0},

	{[]byte{0, 1, 2, 3}, 2, 4, 0, []byte{}, 2, 4},
	{[]byte{0, 1, 2, 3}, 2, 4, 1, []byte{2}, 3, 3},
	{[]byte{0, 1, 2, 3}, 2, 4, 2, []byte{2, 3}, 0, 2},
	{[]byte{0, 1, 2, 3}, 2, 4, 3, []byte{2, 3, 0}, 1, 1},
	{[]byte{0, 1, 2, 3}, 2, 4, 4, []byte{2, 3, 0, 1}, 0, 0},
	{[]byte{0, 1, 2, 3}, 2, 4, 5, []byte{2, 3, 0, 1}, 0, 0},
}

func TestBufferRead(t *testing.T) {
	var b buffer
	for index, c := range bufferReadTestCases {
		b = makebuffer(c.data, c.off, c.size)
		data := make([]byte, c.readsize)
		taken := b.read(data)
		if taken != len(c.expected) {
			t.Errorf("read case %d: read returned %d (expected %d)", index, taken, len(c.expected))
			continue
		}
		data = data[:taken]
		if !reflect.DeepEqual(data, c.expected) {
			t.Errorf("read case %d: read returned %v (expected %v)", index, data, c.expected)
			continue
		}
		if b.off != c.newoff {
			t.Errorf("read case %d: new offset = %d (expected %d)", index, b.off, c.newoff)
		}
		if b.len() != c.newsize {
			t.Errorf("read case %d: new size = %d (expected %d)", index, b.len(), c.newsize)
		}
	}
}

type bufferWriteTestCase struct {
	data     []byte
	off      int
	size     int
	src      []byte
	newoff   int
	newsize  int
	expected []byte
}

var bufferWriteTestCases = []bufferWriteTestCase{
	{[]byte{}, 0, 0, []byte{}, 0, 0, []byte{}},
	{[]byte{}, 0, 0, []byte{1}, 0, 1, []byte{1}},
	{[]byte{}, 0, 0, []byte{1, 2}, 0, 2, []byte{1, 2}},
	{[]byte{}, 0, 0, []byte{1, 2, 3}, 0, 3, []byte{1, 2, 3, 0}},

	{[]byte{0, 1, 2, 3}, 0, 2, []byte{}, 0, 2, []byte{0, 1, 2, 3}},
	{[]byte{0, 1, 2, 3}, 0, 2, []byte{4}, 0, 3, []byte{0, 1, 4, 3}},
	{[]byte{0, 1, 2, 3}, 0, 2, []byte{4, 5}, 0, 4, []byte{0, 1, 4, 5}},
	{[]byte{0, 1, 2, 3}, 0, 2, []byte{4, 5, 6}, 0, 5, []byte{0, 1, 4, 5, 6, 0, 0, 0}},

	{[]byte{0, 1, 2, 3}, 1, 2, []byte{}, 1, 2, []byte{0, 1, 2, 3}},
	{[]byte{0, 1, 2, 3}, 1, 2, []byte{4}, 1, 3, []byte{0, 1, 2, 4}},
	{[]byte{0, 1, 2, 3}, 1, 2, []byte{4, 5}, 1, 4, []byte{5, 1, 2, 4}},
	{[]byte{0, 1, 2, 3}, 1, 2, []byte{4, 5, 6}, 0, 5, []byte{1, 2, 4, 5, 6, 0, 0, 0}},

	{[]byte{0, 1, 2, 3}, 2, 2, []byte{}, 2, 2, []byte{0, 1, 2, 3}},
	{[]byte{0, 1, 2, 3}, 2, 2, []byte{4}, 2, 3, []byte{4, 1, 2, 3}},
	{[]byte{0, 1, 2, 3}, 2, 2, []byte{4, 5}, 2, 4, []byte{4, 5, 2, 3}},
	{[]byte{0, 1, 2, 3}, 2, 2, []byte{4, 5, 6}, 0, 5, []byte{2, 3, 4, 5, 6, 0, 0, 0}},

	{[]byte{0, 1, 2, 3}, 2, 3, []byte{}, 2, 3, []byte{0, 1, 2, 3}},
	{[]byte{0, 1, 2, 3}, 2, 3, []byte{4}, 2, 4, []byte{0, 4, 2, 3}},
	{[]byte{0, 1, 2, 3}, 2, 3, []byte{4, 5}, 0, 5, []byte{2, 3, 0, 4, 5, 0, 0, 0}},
}

func TestBufferWrite(t *testing.T) {
	var b buffer
	for index, c := range bufferWriteTestCases {
		b = makebuffer(c.data, c.off, c.size)
		b.write(c.src)
		if b.off != c.newoff {
			t.Errorf("write case %d: new offset = %d (expected %d)", index, b.off, c.newoff)
		}
		if b.len() != c.newsize {
			t.Errorf("write case %d: new size = %d (expected %d)", index, b.len(), c.newsize)
		}
		data := b.buf[:cap(b.buf)]
		if !reflect.DeepEqual(data, c.expected) {
			t.Errorf("write case %d: new data = %v (expected %v)", index, data, c.expected)
		}
		b.clear()
		if b.buf != nil || b.off != 0 {
			t.Errorf("write case %d: clear failed", index)
		}
	}
}
