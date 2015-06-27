package copper

import (
	"bufio"
	"time"
)

type connReader struct {
	owner  *rawConn
	buffer *bufio.Reader
}

func newConnReader(owner *rawConn) *connReader {
	r := &connReader{
		owner: owner,
	}
	r.buffer = bufio.NewReader(r)
	return r
}

func (r *connReader) Read(b []byte) (int, error) {
	r.owner.conn.SetReadDeadline(time.Now().Add(r.owner.localInactivityTimeout))
	return r.owner.conn.Read(b)
}
