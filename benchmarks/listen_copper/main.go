package main

import (
	"github.com/snaury/copper"
	"io"
	"log"
	"net"
	"os"
)

const (
	listenAddr = "./listen_copper.sock"
)

func process(stream copper.Stream) {
	var err error
	defer stream.Close()
	addr := stream.RemoteAddr()
	log.Printf("accepted stream from %s", addr)
	var buf [65536]byte
	for {
		_, err = stream.Write(buf[:])
		if err != nil {
			log.Printf("error writing to %s: %s", addr, err)
			return
		}
	}
}

func provideLatency(stream copper.Stream) {
	var err error
	defer stream.Close()
	addr := stream.RemoteAddr()
	var buf [8]byte
	for {
		_, err = io.ReadFull(stream, buf[0:8])
		if err != nil {
			if err != io.EOF {
				log.Printf("error reading from %s: %s", addr, err)
			}
			return
		}
		_, err = stream.Write(buf[0:8])
		if err != nil {
			log.Printf("error writing to %s: %s", addr, err)
			return
		}
	}
}

func handleStream(stream copper.Stream) {
	switch stream.TargetID() {
	case 0:
		process(stream)
	case 1:
		provideLatency(stream)
	default:
		stream.CloseWithError(copper.ENOTARGET)
	}
}

func handleConn(rawconn net.Conn) {
	addr := rawconn.RemoteAddr()
	log.Printf("accepted connection from %s", addr)
	conn := copper.NewConn(rawconn, copper.StreamHandlerFunc(handleStream), true)
	defer conn.Close()
	err := conn.Wait()
	log.Printf("connection from %s closed: %s", addr, err)
}

func main() {
	os.Remove(listenAddr)
	l, err := net.Listen("unix", listenAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	log.Printf("Listening...")
	for {
		conn, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go handleConn(conn)
	}
}
