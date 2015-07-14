package benchmark

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"time"

	"github.com/snaury/copper"
)

func startCopper(addr string) (string, func()) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen: %s", err)
	}
	srv := copper.NewServer()
	err = srv.AddListener(listener, true)
	if err != nil {
		log.Fatalf("Failed to add a listener: %s", err)
	}
	return listener.Addr().String(), func() {
		srv.Close()
	}
}

func connectCopper(addr string) copper.Client {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to connect: %s", err)
	}
	return copper.NewClient(conn)
}

func publishCopperService(addr string, concurrency int) func() {
	client := connectCopper(addr)
	pub, err := client.Publish(
		"test:myservice",
		copper.PublishSettings{
			Concurrency: uint32(concurrency),
			QueueSize:   uint32(concurrency * 2),
		},
		copper.HandlerFunc(func(stream copper.Stream) {
			var buf [8]byte
			_, err := io.ReadFull(stream, buf[:])
			if err != nil && err != io.EOF {
				stream.CloseWithError(err)
				return
			}
			stream.Write(buf[:])
		}),
	)
	if err != nil {
		log.Fatalf("Failed to publish: %s", err)
	}
	return func() {
		pub.Stop()
		client.Close()
	}
}

func publishCopperWriter(addr string) func() {
	client := connectCopper(addr)
	pub, err := client.Publish(
		"test:myservice",
		copper.PublishSettings{
			Concurrency: 1,
			QueueSize:   1,
		},
		copper.HandlerFunc(func(stream copper.Stream) {
			var buf [65536]byte
			for {
				_, err := stream.Write(buf[:])
				if err != nil {
					break
				}
			}
		}),
	)
	if err != nil {
		log.Fatalf("Failed to publish: %s", err)
	}
	return func() {
		pub.Stop()
		client.Close()
	}
}

func subscribeCopperService(addr string) (copper.Subscription, func()) {
	client := connectCopper(addr)
	sub, err := client.Subscribe(copper.SubscribeSettings{
		Options: []copper.SubscribeOption{
			{Service: "test:myservice"},
		},
	})
	if err != nil {
		log.Fatalf("Failed to subscribe: %s", err)
	}
	return sub, func() {
		sub.Stop()
		client.Close()
	}
}

func callCopperService(sub copper.Subscription) {
	stream, err := sub.Open()
	if err != nil {
		log.Fatalf("Failed to open stream: %s", err)
	}
	defer stream.Close()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(time.Now().UnixNano()))
	_, err = stream.Write(buf[:])
	if err != nil {
		log.Fatalf("Failed to write: %s", err)
	}
	stream.CloseWrite()
	_, err = io.ReadFull(stream, buf[:])
	if err != nil {
		log.Fatalf("Failed to read: %s", err)
	}
}

func benchreadCopperService(sub copper.Subscription, totalBytes int64) {
	stream, err := sub.Open()
	if err != nil {
		log.Fatalf("Failed to open stream: %s", err)
	}
	defer stream.Close()
	var buf [65536]byte
	for totalBytes > 0 {
		n, err := stream.Read(buf[:])
		if err != nil {
			log.Fatalf("Failed to read: %s", err)
		}
		totalBytes -= int64(n)
	}
}
