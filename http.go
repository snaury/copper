package copper

import (
	"net"
	"net/http"
)

type fakeListener struct {
	stream   Stream
	accepted bool
}

func (l *fakeListener) Accept() (net.Conn, error) {
	if l.accepted {
		<-l.stream.WriteClosed()
		return nil, ECLOSED
	}
	l.accepted = true
	return l.stream, nil
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

func (h httpHandler) ServeCopper(stream Stream) {
	s := &http.Server{
		Handler: h.handler,
	}
	s.Serve(&fakeListener{
		stream: stream,
	})
}

// HTTPHandler returns a handler that servers http requests with an http.Handler
func HTTPHandler(handler http.Handler) Handler {
	return httpHandler{
		handler: handler,
	}
}
