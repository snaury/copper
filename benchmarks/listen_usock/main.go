package main

import (
	"bufio"
	"log"
	"net"
	"os"
)

const (
	listenAddr = "./listen_usock.sock"
)

func process(conn net.Conn) {
	defer conn.Close()
	addr := conn.RemoteAddr()
	log.Printf("accepted connection from %s", addr)
	var buf [65536]byte
	w := bufio.NewWriter(conn)
	for {
		_, err := w.Write(buf[:])
		if err != nil {
			log.Printf("error writing to %s: %s", addr, err)
			return
		}
	}
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
		go process(conn)
	}
}
