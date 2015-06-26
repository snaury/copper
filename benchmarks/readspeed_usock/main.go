package main

import (
	"bufio"
	"log"
	"net"
	"time"
)

const (
	dialAddr         = "../listen_usock/listen_usock.sock"
	GB               = 1024 * 1024 * 1024
	reportEveryBytes = 1024 * 1024 * 1024
)

func process(conn net.Conn) {
	defer conn.Close()
	addr := conn.RemoteAddr()
	log.Printf("connected to %s", addr)
	r := bufio.NewReader(conn)
	var buf [65536]byte
	var total int64
	var current int64
	tstart := time.Now()
	for {
		n, err := r.Read(buf[:])
		current += int64(n)
		if current >= reportEveryBytes {
			tnow := time.Now()
			log.Printf("reading from %s: %.3fGB/s", addr, float64(current)/float64(GB)/tnow.Sub(tstart).Seconds())
			total += current
			current = 0
			tstart = tnow
		}
		if err != nil {
			log.Printf("error reading from %s: %s", addr, err)
			return
		}
	}
}

func main() {
	conn, err := net.Dial("unix", dialAddr)
	if err != nil {
		log.Fatal(err)
	}
	process(conn)
}
