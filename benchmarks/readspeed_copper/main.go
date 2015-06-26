package main

import (
	"flag"
	"github.com/snaury/copper"
	"log"
	"net"
	"time"
)

const (
	dialAddr = "../listen_copper/listen_copper.sock"
	gb       = 1024 * 1024 * 1024
)

func process(stream copper.Stream, delay int) {
	defer stream.Close()
	addr := stream.RemoteAddr()
	log.Printf("connected to %s", addr)
	var buf [65536]byte
	var total int64
	var current int64
	tstart := time.Now()
	for {
		if delay > 0 {
			time.Sleep(time.Duration(delay) * time.Second)
		}
		n, err := stream.Read(buf[:])
		current += int64(n)
		tnow := time.Now()
		elapsed := tnow.Sub(tstart)
		if elapsed >= time.Second {
			log.Printf("reading from %s: %.3fGB/s", addr, float64(current)/float64(gb)/elapsed.Seconds())
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

var delay = flag.Int("delay", 0, "delay between first stream reads in seconds ")
var extra = flag.Int("extra", 0, "number of extra streams without delay")

func main() {
	flag.Parse()
	rawconn, err := net.Dial("unix", dialAddr)
	if err != nil {
		log.Fatal(err)
	}
	conn := copper.NewConn(rawconn, nil, false)
	defer conn.Close()

	for i := 0; i < *extra; i++ {
		stream, err := conn.OpenStream(0)
		if err != nil {
			log.Fatal(err)
		}
		go process(stream, 0)
	}

	stream, err := conn.OpenStream(0)
	if err != nil {
		log.Fatal(err)
	}
	process(stream, *delay)
}
