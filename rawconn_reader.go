package copper

import (
	"time"
)

type rawConnReader struct {
	conn *rawConn
}

func newRawConnReader(conn *rawConn) *rawConnReader {
	return &rawConnReader{
		conn: conn,
	}
}

func (r *rawConnReader) Read(b []byte) (int, error) {
	r.conn.lastread = time.Now()
	r.conn.conn.SetReadDeadline(r.conn.lastread.Add(r.conn.settings.getLocalInactivityTimeout()))
	return r.conn.conn.Read(b)
}
