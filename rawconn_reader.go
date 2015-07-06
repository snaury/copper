package copper

import (
	"bufio"
	"time"
)

type rawConnReader struct {
	owner  *rawConn
	buffer *bufio.Reader
}

func newRawConnReader(owner *rawConn) *rawConnReader {
	r := &rawConnReader{
		owner: owner,
	}
	r.buffer = bufio.NewReader(r)
	return r
}

func (r *rawConnReader) Read(b []byte) (int, error) {
	r.owner.conn.SetReadDeadline(time.Now().Add(r.owner.localInactivityTimeout))
	return r.owner.conn.Read(b)
}
