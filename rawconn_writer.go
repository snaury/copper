package copper

import (
	"time"
)

type rawConnWriter struct {
	conn *rawConn
}

func newRawConnWriter(conn *rawConn) *rawConnWriter {
	return &rawConnWriter{
		conn: conn,
	}
}

func (w *rawConnWriter) Write(p []byte) (int, error) {
	w.conn.lastwrite = time.Now()
	w.conn.conn.SetWriteDeadline(w.conn.lastwrite.Add(w.conn.settings.getLocalInactivityTimeout()))
	return w.conn.conn.Write(p)
}
