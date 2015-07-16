package copper

import (
	"time"
)

type rawConnWriter struct {
	owner *rawConn
}

func newRawConnWriter(owner *rawConn) *rawConnWriter {
	return &rawConnWriter{
		owner: owner,
	}
}

func (w *rawConnWriter) Write(p []byte) (int, error) {
	w.owner.lastwrite = time.Now()
	w.owner.conn.SetWriteDeadline(w.owner.lastwrite.Add(w.owner.settings.getLocalInactivityTimeout()))
	return w.owner.conn.Write(p)
}
