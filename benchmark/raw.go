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
	return conn.Wait()
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

func (s *rawServer) Handle(stream copper.Stream) {
	switch stream.TargetID() {
	case 1:
		var buf [8]byte
		_, err := io.ReadFull(stream, buf[:])
		if err != nil && err != io.EOF {
			stream.CloseWithError(err)
			return
		}
		stream.Write(buf[:])
	case 2:
		var buf [65536]byte
		for {
			_, err := stream.Write(buf[:])
			if err != nil {
				break
			}
		}
	default:
		stream.CloseWithError(copper.ENOTARGET)
	}
}

func startRawServer(addr string) (string, func()) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen: %s", err)
	}
	s := &rawServer{}
	s.clients = make(map[copper.RawConn]struct{})
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
	stream, err := conn.Open(1)
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
	stream, err := conn.Open(2)
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
