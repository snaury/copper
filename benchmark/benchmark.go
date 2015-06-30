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

type server struct {
	lock     sync.Mutex
	listener net.Listener
	clients  map[copper.Conn]struct{}
}

func (s *server) handle(rawConn net.Conn) error {
	s.lock.Lock()
	if s.listener == nil {
		s.lock.Unlock()
		rawConn.Close()
		return nil
	}
	conn := copper.NewConn(rawConn, s, true)
	s.clients[conn] = struct{}{}
	s.lock.Unlock()
	defer func() {
		s.lock.Lock()
		delete(s.clients, conn)
		s.lock.Unlock()
	}()
	return conn.Wait()
}

func (s *server) Serve(listener net.Listener) error {
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

func (s *server) Stop() {
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

func (s *server) HandleStream(stream copper.Stream) {
	var buf [8]byte
	_, err := io.ReadFull(stream, buf[:])
	if err != nil {
		stream.CloseWithError(err)
		return
	}
	stream.Write(buf[:])
}

func startServer(addr string) (string, func()) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen: %s", err)
	}
	s := &server{}
	s.clients = make(map[copper.Conn]struct{})
	go s.Serve(listener)
	return listener.Addr().String(), func() {
		s.Stop()
	}
}

func dial(addr string) copper.Conn {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to Dial: %s", err)
	}
	return copper.NewConn(c, nil, false)
}

func call(conn copper.Conn) {
	stream, err := conn.Open(0)
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
