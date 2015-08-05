package copper

import (
	"net"
	"net/http"
)

type fakeListenerConn struct {
	net.Conn
	listener *fakeListener
}

func (s *fakeListenerConn) Close() error {
	err := s.Conn.Close()
	if err == nil {
		close(s.listener.closed)
	}
	return err
}

type fakeListener struct {
	stream   Stream
	accepted bool
	closed   chan struct{}
}

func (l *fakeListener) Accept() (net.Conn, error) {
	if l.accepted {
		// Wait until close is called on a previously accepted connection
		<-l.closed
		return nil, ECLOSED
	}
	l.accepted = true
	l.closed = make(chan struct{})
	return &fakeListenerConn{l.stream, l}, nil
}

func (l *fakeListener) Close() error {
	return nil
}

func (l *fakeListener) Addr() net.Addr {
	return l.stream.LocalAddr()
}

type httpHandler struct {
	handler http.Handler
}

func (h httpHandler) ServeCopper(stream Stream) error {
	s := &http.Server{
		Handler: h.handler,
	}
	s.Serve(&fakeListener{
		stream: stream,
	})
	return nil
}

// HTTPHandler returns a handler that servers http requests with an http.Handler
func HTTPHandler(handler http.Handler) Handler {
	return httpHandler{
		handler: handler,
	}
}
