package main

import (
	"flag"
	"github.com/snaury/copper"
	"io"
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

func measurePing(conn copper.Conn) {
	defer conn.Close()
	var maxlatency int64
	var sumlatency int64
	var count int64
	tstart := time.Now()
	for {
		t0 := time.Now()
		err := <-conn.Ping(123)
		if err != nil {
			log.Printf("ping failed: %s", err)
			return
		}
		t1 := time.Now()

		latency := t1.Sub(t0).Nanoseconds()
		if maxlatency < latency {
			maxlatency = latency
		}
		sumlatency += latency
		count++

		elapsed := t1.Sub(tstart)
		if elapsed > time.Second {
			log.Printf("measured latency: %dns (%dns avg over %d)", maxlatency, sumlatency/count, count)
			maxlatency = 0
			sumlatency = 0
			count = 0
			tstart = t1
		}
	}
}

func measureLatency(conn copper.Conn) {
	var err error
	defer conn.Close()
	stream, err := conn.OpenStream(1)
	if err != nil {
		log.Fatal(err)
	}
	addr := stream.RemoteAddr()
	log.Printf("connected to %s", addr)
	var buf [8]byte
	var maxlatency int64
	var sumlatency int64
	var count int64
	tstart := time.Now()
	for {
		t0 := time.Now()
		_, err = stream.Write(buf[0:8])
		if err != nil {
			log.Printf("error writing to %s: %s", addr, err)
			return
		}
		_, err = io.ReadFull(stream, buf[0:8])
		if err != nil {
			log.Printf("error reading from %s: %s", addr, err)
		}
		t1 := time.Now()

		latency := t1.Sub(t0).Nanoseconds()
		if maxlatency < latency {
			maxlatency = latency
		}
		sumlatency += latency
		count++

		elapsed := t1.Sub(tstart)
		if elapsed > time.Second {
			log.Printf("measured latency: %dns (%dns avg over %d)", maxlatency, sumlatency/count, count)
			maxlatency = 0
			sumlatency = 0
			count = 0
			tstart = t1
		}
	}
}

func measureLatencyOneShots(conn copper.Conn) {
	defer conn.Close()
	var buf [8]byte
	var maxlatency int64
	var sumlatency int64
	var count int64
	tstart := time.Now()
	for {
		t0 := time.Now()
		stream, err := conn.OpenStream(1)
		if err != nil {
			log.Printf("cannot open stream: %s", err)
			return
		}
		_, err = stream.Write(buf[0:8])
		if err != nil {
			log.Printf("error writing to %s: %s", stream.RemoteAddr(), err)
			stream.Close()
			return
		}
		_, err = io.ReadFull(stream, buf[0:8])
		if err != nil {
			log.Printf("error reading from %s: %s", stream.RemoteAddr(), err)
			stream.Close()
			return
		}
		stream.Close()
		t1 := time.Now()

		latency := t1.Sub(t0).Nanoseconds()
		if maxlatency < latency {
			maxlatency = latency
		}
		sumlatency += latency
		count++

		elapsed := t1.Sub(tstart)
		if elapsed > time.Second {
			log.Printf("measured latency: %dns (%dns avg over %d) @ %s", maxlatency, sumlatency/count, count, stream.LocalAddr())
			maxlatency = 0
			sumlatency = 0
			count = 0
			tstart = t1
		}
	}
}

var delay = flag.Int("delay", 0, "delay between first stream reads in seconds ")
var extra = flag.Int("extra", 0, "number of extra streams without delay")
var ping = flag.Bool("ping", false, "measure latency using ping frames")
var latency = flag.Bool("latency", false, "measure latency using normal streams")
var oneshots = flag.Bool("oneshots", false, "measure latency using oneshot streams")

func main() {
	flag.Parse()
	rawconn, err := net.Dial("unix", dialAddr)
	if err != nil {
		log.Fatal(err)
	}
	conn := copper.NewConn(rawconn, nil, false)
	defer conn.Close()

	if *ping {
		measurePing(conn)
		return
	}

	if *latency {
		if *oneshots {
			measureLatencyOneShots(conn)
		} else {
			measureLatency(conn)
		}
		return
	}

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
