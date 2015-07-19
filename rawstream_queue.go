package copper

// rawStreamQueue is a ring-buffer queue of rawStream pointers
type rawStreamQueue struct {
	buf  []*rawStream
	off  int
	size int
}

// push adds a pointer to the end of the queue, expanding it when necessary
func (q *rawStreamQueue) push(s *rawStream) {
	pos := q.off + q.size
	if q.size == len(q.buf) {
		dst := make([]*rawStream, minpow2(len(q.buf)+1))
		if pos == len(q.buf) {
			// queue doesn't wrap, simple copy
			copy(dst, q.buf)
		} else {
			// queue wraps, copy with two steps
			z := copy(dst, q.buf[q.off:])
			copy(dst[z:], q.buf[:q.size-z])
			pos = q.size
		}
		q.buf = dst
		q.off = 0
	} else if pos >= len(q.buf) {
		pos -= len(q.buf)
	}
	q.buf[pos] = s
	q.size++
}

// take removes a pointer from the front of the queue
func (q *rawStreamQueue) take() *rawStream {
	if q.size == 0 {
		return nil
	}
	stream := q.buf[q.off]
	q.buf[q.off] = nil
	q.off++
	q.size--
	if q.off == len(q.buf) || q.size == 0 {
		q.off = 0
	}
	return stream
}
