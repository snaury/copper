package benchmark

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"time"

	"github.com/snaury/copper/copperd"
	"github.com/snaury/copper/raw"
)

func startCopperd(addr string) (string, func()) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen: %s", err)
	}
	srv := copperd.NewServer()
	err = srv.AddListener(listener)
	if err != nil {
		log.Fatalf("Failed to add a listener: %s", err)
	}
	go srv.Serve()
	return listener.Addr().String(), func() {
		srv.Shutdown()
	}
}

func connectCopperd(addr string) copperd.Client {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to connect: %s", err)
	}
	return copperd.NewClient(conn)
}

func publishCopperdService(addr string, concurrency int) func() {
	client := connectCopperd(addr)
	pub, err := client.Publish(
		"test:myservice",
		copperd.PublishSettings{
			Concurrency: uint32(concurrency),
			QueueSize:   uint32(concurrency * 2),
		},
		raw.StreamHandlerFunc(func(stream raw.Stream) {
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

func subscribeCopperdService(addr string) (copperd.Subscription, func()) {
	client := connectCopperd(addr)
	sub, err := client.Subscribe(copperd.SubscribeSettings{
		Options: []copperd.SubscribeOption{
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

func callCopperdService(sub copperd.Subscription) {
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
