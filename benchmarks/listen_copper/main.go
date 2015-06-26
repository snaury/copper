package main

import (
	"github.com/snaury/copper"
	"log"
	"net"
	"os"
)

const (
	listenAddr = "./listen_copper.sock"
)

func process(stream copper.Stream) {
	defer stream.Close()
	addr := stream.RemoteAddr()
	log.Printf("accepted stream from %s", addr)
	var buf [65536]byte
	for {
		_, err := stream.Write(buf[:])
		if err != nil {
			log.Printf("error writing to %s: %s", addr, err)
			return
		}
	}
}

func handleStream(_ int64, stream copper.Stream) {
	process(stream)
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
