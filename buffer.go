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

// clear discards all buffer memory
func (b *buffer) clear() {
	b.buf = nil
	b.off = 0
}
