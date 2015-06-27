package copper

import (
	"bufio"
	"time"
)

type connWriter struct {
	owner   *rawConn
	buffer  *bufio.Writer
	written bool
}

func newConnWriter(owner *rawConn) *connWriter {
	w := &connWriter{
		owner: owner,
	}
	w.buffer = bufio.NewWriter(w)
	return w
}

func (w *connWriter) Write(p []byte) (int, error) {
	w.owner.lock.Unlock()
	defer w.owner.lock.Lock()
	w.written = true
	w.owner.conn.SetWriteDeadline(time.Now().Add(w.owner.localInactivityTimeout))
	return w.owner.conn.Write(p)
}
