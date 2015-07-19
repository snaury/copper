package benchmark

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/snaury/copper"
)

type rawServer struct {
	lock     sync.Mutex
	listener net.Listener
	clients  map[copper.RawConn]struct{}
	kind     int
}

func (s *rawServer) handle(rawConn net.Conn) error {
	s.lock.Lock()
	if s.listener == nil {
		s.lock.Unlock()
		rawConn.Close()
		return nil
	}
	conn := copper.NewRawConn(rawConn, s, true)
	s.clients[conn] = struct{}{}
	s.lock.Unlock()
	defer func() {
		s.lock.Lock()
		delete(s.clients, conn)
		s.lock.Unlock()
	}()
	<-conn.Done()
	return conn.Err()
}

func (s *rawServer) Serve(listener net.Listener) error {
	s.listener = listener
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go s.handle(conn)
	}
}

func (s *rawServer) Stop() {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.listener != nil {
		s.listener.Close()
		s.listener = nil
	}
	for conn := range s.clients {
		delete(s.clients, conn)
		conn.Close()
	}
}

func (s *rawServer) ServeCopper(stream copper.Stream) error {
	switch s.kind {
	case 0:
		var buf [8]byte
		_, err := io.ReadFull(stream, buf[:])
		if err != nil && err != io.EOF {
			return err
		}
		stream.Write(buf[:])
		return nil
	case 1:
		var buf [65536]byte
		for {
			_, err := stream.Write(buf[:])
			if err != nil {
				return err
			}
		}
	default:
		return copper.ENOTARGET
	}
}

func startRawServer(addr string, kind int) (string, func()) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen: %s", err)
	}
	s := &rawServer{
		clients: make(map[copper.RawConn]struct{}),
		kind:    kind,
	}
	go s.Serve(listener)
	return listener.Addr().String(), func() {
		s.Stop()
	}
}

func dialRawServer(addr string) copper.RawConn {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to Dial: %s", err)
	}
	return copper.NewRawConn(c, nil, false)
}

func callRawServer(conn copper.RawConn) {
	stream, err := conn.NewStream()
	if err != nil {
		log.Fatalf("Failed to Open: %s", err)
	}
	defer stream.Close()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(time.Now().UnixNano()))
	_, err = stream.Write(buf[:])
	if err != nil {
		log.Fatalf("Failed to Write: %s", err)
	}
	stream.CloseWrite()
	_, err = io.ReadFull(stream, buf[:])
	if err != nil {
		log.Fatalf("Failed to ReadFull: %s", err)
	}
}

func benchreadRawServer(conn copper.RawConn, totalBytes int64) {
	stream, err := conn.NewStream()
	if err != nil {
		log.Fatalf("Failed to Open: %s", err)
	}
	defer stream.Close()
	var buf [65536]byte
	for totalBytes > 0 {
		n, err := stream.Read(buf[:])
		if err != nil {
			log.Fatalf("Failed to Read: %s", err)
		}
		totalBytes -= int64(n)
	}
}
