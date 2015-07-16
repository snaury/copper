package copper

import (
	"time"
)

type rawConnReader struct {
	owner *rawConn
}

func newRawConnReader(owner *rawConn) *rawConnReader {
	return &rawConnReader{
		owner: owner,
	}
}

func (r *rawConnReader) Read(b []byte) (int, error) {
	r.owner.lastread = time.Now()
	r.owner.conn.SetReadDeadline(r.owner.lastread.Add(r.owner.settings.getLocalInactivityTimeout()))
	return r.owner.conn.Read(b)
}
