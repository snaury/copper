package main

import (
	"io"
	"log"
	"net"
	"os"

	"github.com/snaury/copper/raw"
)

const (
	listenAddr = "./listen_copper.sock"
)

func process(stream raw.Stream) {
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

func provideLatency(stream raw.Stream) {
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

func handleStream(stream raw.Stream) {
	switch stream.TargetID() {
	case 0:
		process(stream)
	case 1:
		provideLatency(stream)
	default:
		stream.CloseWithError(raw.ENOTARGET)
	}
}

func handleConn(rawconn net.Conn) {
	addr := rawconn.RemoteAddr()
	log.Printf("accepted connection from %s", addr)
	conn := raw.NewConn(rawconn, raw.StreamHandlerFunc(handleStream), true)
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
