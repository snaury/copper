package main

import (
	"bufio"
	"flag"
	"io"
	"log"
	"net"
	"os"
)

const (
	listenAddr = "./listen_usock.sock"
)

func process(conn net.Conn) {
	var err error
	defer conn.Close()
	addr := conn.RemoteAddr()
	log.Printf("accepted connection from %s", addr)
	var buf [65536]byte
	w := bufio.NewWriter(conn)
	for {
		_, err = w.Write(buf[:])
		if err != nil {
			log.Printf("error writing to %s: %s", addr, err)
			return
		}
	}
}

func provideLatency(conn net.Conn) {
	var err error
	defer conn.Close()
	addr := conn.RemoteAddr()
	log.Printf("accepted connection from %s", addr)
	var buf [8]byte
	r := bufio.NewReader(conn)
	for {
		_, err = io.ReadFull(r, buf[0:8])
		if err != nil {
			log.Printf("error reading from %s: %s", addr, err)
			return
		}
		_, err = conn.Write(buf[0:8])
		if err != nil {
			log.Printf("error writing to %s: %s", addr, err)
		}
	}
}

var latency = flag.Bool("latency", false, "write data suitable for latency calculation")

func main() {
	flag.Parse()
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
		if *latency {
			go provideLatency(conn)
		} else {
			go process(conn)
		}
	}
}
