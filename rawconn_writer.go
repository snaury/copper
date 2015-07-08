package copper

import (
	"bufio"
	"time"
)

type rawConnWriter struct {
	owner  *rawConn
	buffer *bufio.Writer
	writes int
}

func newRawConnWriter(owner *rawConn) *rawConnWriter {
	w := &rawConnWriter{
		owner: owner,
	}
	w.buffer = bufio.NewWriterSize(w, defaultConnBufferSize)
	return w
}

func (w *rawConnWriter) Write(p []byte) (int, error) {
	w.writes++
	w.owner.lock.Unlock()
	defer w.owner.lock.Lock()
	w.owner.conn.SetWriteDeadline(time.Now().Add(w.owner.localInactivityTimeout))
	return w.owner.conn.Write(p)
}
