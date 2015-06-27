package copper

import (
	"bufio"
	"time"
)

type connWriter struct {
	owner  *rawConn
	buffer *bufio.Writer
	writes int
}

func newConnWriter(owner *rawConn) *connWriter {
	w := &connWriter{
		owner: owner,
	}
	w.buffer = bufio.NewWriter(w)
	return w
}

func (w *connWriter) Write(p []byte) (int, error) {
	w.writes++
	w.owner.lock.Unlock()
	defer w.owner.lock.Lock()
	w.owner.conn.SetWriteDeadline(time.Now().Add(w.owner.localInactivityTimeout))
	return w.owner.conn.Write(p)
}
