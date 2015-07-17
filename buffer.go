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
	buf []byte
	off int
}

// len returns amount of data in the buffer
func (b *buffer) len() int {
	return len(b.buf)
}

// read takes up to len(dst) bytes from the buffer
func (b *buffer) read(dst []byte) (n int) {
	end := b.off + len(b.buf)
	if end <= cap(b.buf) {
		// data does not wrap, keep it simple
		n = copy(dst, b.buf[b.off:end])
		if n == len(b.buf) {
			b.buf = b.buf[:0]
			b.off = 0
		} else {
			b.buf = b.buf[:len(b.buf)-n]
			b.off += n
		}
	} else {
		// data wraps, first copy everything up to cap(b.buf)
		n = copy(dst, b.buf[b.off:cap(b.buf)])
		b.off += n
		if b.off == cap(b.buf) {
			// if we reached cap(b.buf) there is more data
			b.off = copy(dst[n:], b.buf[:len(b.buf)-n])
			n += b.off
		}
		if n == len(b.buf) {
			b.buf = b.buf[:0]
			b.off = 0
		} else {
			b.buf = b.buf[:len(b.buf)-n]
		}
	}
	return
}

// readbyte takes a byte from the buffer
func (b *buffer) readbyte() (c byte) {
	c = b.buf[:cap(b.buf)][b.off]
	if len(b.buf) == 1 {
		b.buf = b.buf[:0]
		b.off = 0
	} else {
		b.buf = b.buf[:len(b.buf)-1]
		b.off++
		if b.off == cap(b.buf) {
			b.off = 0
		}
	}
	return c
}

// write adds len(src) bytes to the buffer
func (b *buffer) write(src []byte) {
	m := len(b.buf)
	n := len(src)
	end := b.off + m
	if m+n > cap(b.buf) {
		dst := make([]byte, minpow2(m+n))
		if end <= cap(b.buf) {
			// data does not wrap, need simple copy
			copy(dst, b.buf[b.off:end])
		} else {
			// data wraps, need to copy two parts
			z := copy(dst, b.buf[b.off:cap(b.buf)])
			copy(dst[z:], b.buf[:m-z])
		}
		copy(dst[m:], src)
		b.buf = dst[:m+n]
		b.off = 0
		return
	}
	if end >= cap(b.buf) {
		// data already wraps, so new data cannot
		copy(b.buf[end-cap(b.buf):cap(b.buf)], src)
	} else {
		newend := end + n
		if newend <= cap(b.buf) {
			// new data does not wrap, need simple copy
			copy(b.buf[end:newend], src)
		} else {
			// new data wraps, need to copy two parts
			z := copy(b.buf[end:cap(b.buf)], src)
			copy(b.buf[:cap(b.buf)], src[z:])
		}
	}
	b.buf = b.buf[:m+n]
}

// writebyte adds a byte to the buffer
func (b *buffer) writebyte(c byte) {
	m := len(b.buf)
	end := b.off + len(b.buf)
	if m == cap(b.buf) {
		dst := make([]byte, minpow2(m+1))
		if end <= cap(b.buf) {
			// data does not wrap, need simple copy
			copy(dst, b.buf[b.off:end])
		} else {
			// data wraps, need to copy two parts
			z := copy(dst, b.buf[b.off:cap(b.buf)])
			copy(dst[z:], b.buf[:m-z])
		}
		dst[m] = c
		b.buf = dst[:m+1]
		b.off = 0
		return
	}
	if end >= cap(b.buf) {
		// current data wraps
		b.buf[:cap(b.buf)][end-cap(b.buf)] = c
	} else {
		b.buf[:cap(b.buf)][end] = c
	}
	b.buf = b.buf[:m+1]
}

// clear discards all buffer memory
func (b *buffer) clear() {
	b.buf = nil
	b.off = 0
}

// peek returns data as a contiguous slice, moving it when necessary
func (b *buffer) peek() []byte {
	end := b.off + len(b.buf)
	if end > cap(b.buf) {
		// data is not contiguous
		end -= cap(b.buf)
		if len(b.buf)*2 > cap(b.buf) {
			// there's not enough space to move data around
			buf := make([]byte, cap(b.buf)*2)
			n := copy(buf, b.buf[b.off:cap(b.buf)])
			copy(buf[n:], b.buf[:end])
			b.buf = buf[:len(b.buf)]
			b.off = 0
		} else {
			// there's enough space to move data in the middle
			n := copy(b.buf[end:], b.buf[b.off:cap(b.buf)])
			copy(b.buf[end+n:b.off], b.buf[:end])
			b.off = end
		}
		end = b.off + len(b.buf)
	}
	return b.buf[b.off:end]
}

// discard throws away up to n bytes of data as if done by a read call
func (b *buffer) discard(n int) int {
	if n > len(b.buf) {
		n = len(b.buf)
		b.buf = b.buf[:0]
		b.off = 0
		return n
	}
	b.buf = b.buf[:len(b.buf)-n]
	b.off += n
	if b.off >= cap(b.buf) {
		b.off -= cap(b.buf)
	}
	return n
}
