package main

import (
	"bufio"
	"flag"
	"io"
	"log"
	"net"
	"time"
)

const (
	dialAddr = "../listen_usock/listen_usock.sock"
	gb       = 1024 * 1024 * 1024
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

func measureLatency(conn net.Conn) {
	var err error
	defer conn.Close()
	addr := conn.RemoteAddr()
	log.Printf("connected to %s", addr)
	var buf [8]byte
	var maxlatency int64
	var sumlatency int64
	var count int64
	tstart := time.Now()
	for {
		t0 := time.Now()
		_, err = conn.Write(buf[0:8])
		if err != nil {
			log.Printf("error writing to %s: %s", addr, err)
			return
		}
		_, err = io.ReadFull(conn, buf[0:8])
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
			log.Printf("measured latency: %dns (%dns avg)", maxlatency, sumlatency/count)
			maxlatency = 0
			sumlatency = 0
			count = 0
			tstart = t1
		}
	}
}

var latency = flag.Bool("latency", false, "measure response latency")

func main() {
	flag.Parse()
	conn, err := net.Dial("unix", dialAddr)
	if err != nil {
		log.Fatal(err)
	}
	if *latency {
		measureLatency(conn)
	} else {
		process(conn)
	}
}
