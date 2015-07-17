package copper

// minpow2 returns the minimum power of 2 greater than or equal to n
func minpow2(n int) int {
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32
	n++
	return n
}

// buffer represents a very simple dynamic ring buffer for data
type buffer struct {
	buf  []byte
	off  int
	size int
}

// read takes up to len(dst) bytes from the buffer
func (b *buffer) read(dst []byte) (n int) {
	end := b.off + b.size
	if end <= len(b.buf) {
		// data does not wrap, keep it simple
		n = copy(dst, b.buf[b.off:end])
		b.off += n
		b.size -= n
	} else {
		// data wraps, first copy everything up to len(b.buf)
		n = copy(dst, b.buf[b.off:len(b.buf)])
		b.off += n
		if b.off == len(b.buf) {
			// if we reached len(b.buf) there is more data
			b.off = copy(dst[n:], b.buf[:b.size-n])
			n += b.off
		}
		b.size -= n
	}
	if b.size == 0 {
		b.off = 0
	}
	return
}

// readbyte takes a byte from the buffer
func (b *buffer) readbyte() (c byte) {
	c = b.buf[b.off]
	if b.size == 1 {
		b.off = 0
		b.size = 0
	} else {
		b.off++
		b.size--
		if b.off == len(b.buf) {
			b.off = 0
		}
	}
	return c
}

// write adds len(src) bytes to the buffer
func (b *buffer) write(src []byte) {
	m := b.size
	n := len(src)
	end := b.off + m
	if m+n > len(b.buf) {
		dst := make([]byte, minpow2(m+n))
		if end <= len(b.buf) {
			// data does not wrap, need simple copy
			copy(dst, b.buf[b.off:end])
		} else {
			// data wraps, need to copy two parts
			z := copy(dst, b.buf[b.off:len(b.buf)])
			copy(dst[z:], b.buf[:m-z])
		}
		copy(dst[m:], src)
		b.buf = dst
		b.off = 0
	} else if end >= len(b.buf) {
		// data already wraps, so new data cannot
		copy(b.buf[end-len(b.buf):len(b.buf)], src)
	} else {
		newend := end + n
		if newend <= len(b.buf) {
			// new data does not wrap, need simple copy
			copy(b.buf[end:newend], src)
		} else {
			// new data wraps, need to copy two parts
			z := copy(b.buf[end:len(b.buf)], src)
			copy(b.buf[:len(b.buf)], src[z:])
		}
	}
	b.size += n
}

// writebyte adds a byte to the buffer
func (b *buffer) writebyte(c byte) {
	m := b.size
	end := b.off + b.size
	if m == len(b.buf) {
		dst := make([]byte, minpow2(m+1))
		if end <= len(b.buf) {
			// data does not wrap, need simple copy
			copy(dst, b.buf[b.off:end])
		} else {
			// data wraps, need to copy two parts
			z := copy(dst, b.buf[b.off:len(b.buf)])
			copy(dst[z:], b.buf[:m-z])
		}
		dst[m] = c
		b.buf = dst
		b.off = 0
	} else if end >= len(b.buf) {
		// current data wraps
		b.buf[end-len(b.buf)] = c
	} else {
		b.buf[end] = c
	}
	b.size++
}

// clear discards all buffer memory
func (b *buffer) clear() {
	b.buf = nil
	b.off = 0
	b.size = 0
}

// peek returns data as a contiguous slice, moving it when necessary
func (b *buffer) peek() []byte {
	end := b.off + b.size
	if end > len(b.buf) {
		// data is not contiguous
		end -= len(b.buf)
		if b.size*2 > len(b.buf) {
			// there's not enough space to move data around
			dst := make([]byte, len(b.buf)*2)
			n := copy(dst, b.buf[b.off:len(b.buf)])
			copy(dst[n:], b.buf[:end])
			b.buf = dst
			b.off = 0
		} else {
			// there's enough space to move data in the middle
			n := copy(b.buf[end:], b.buf[b.off:len(b.buf)])
			copy(b.buf[end+n:b.off], b.buf[:end])
			b.off = end
		}
		end = b.off + b.size
	}
	return b.buf[b.off:end]
}

// discard throws away up to n bytes of data as if done by a read call
func (b *buffer) discard(n int) int {
	if n > b.size {
		n = b.size
		b.off = 0
		b.size = 0
		return n
	}
	b.off += n
	if b.off >= len(b.buf) {
		b.off -= len(b.buf)
	}
	b.size -= n
	return n
}
