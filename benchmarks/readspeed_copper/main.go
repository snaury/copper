package main

import (
	"github.com/snaury/copper"
	"log"
	"net"
	"time"
)

const (
	dialAddr         = "../listen_copper/listen_copper.sock"
	openTarget       = 0
	GB               = 1024 * 1024 * 1024
	reportEveryBytes = 1024 * 1024 * 1024
)

func process(stream copper.Stream) {
	defer stream.Close()
	addr := stream.RemoteAddr()
	log.Printf("connected to %s", addr)
	var buf [65536]byte
	var total int64
	var current int64
	tstart := time.Now()
	for {
		n, err := stream.Read(buf[:])
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
	rawconn, err := net.Dial("unix", dialAddr)
	if err != nil {
		log.Fatal(err)
	}
	conn := copper.NewConn(rawconn, nil, false)
	defer conn.Close()
	stream, err := conn.OpenStream(0)
	if err != nil {
		log.Fatal(err)
	}
	process(stream)
}
