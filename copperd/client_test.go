package copperd

import (
	"io"
	"log"
	"net"
	"testing"

	"github.com/snaury/copper"
)

func runServer(address string) (string, func()) {
	server := NewServer()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Failed to listen: %s", err)
	}
	err = server.AddListeners(listener)
	if err != nil {
		log.Fatalf("Failed to add listeners: %s", err)
	}
	go server.Serve()
	return listener.Addr().String(), func() {
		server.Shutdown()
	}
}

func connect(address string) Client {
	client, err := dialClient("tcp", address)
	if err != nil {
		log.Fatalf("Failed to connect: %s", err)
	}
	return client
}

func TestClientServer(t *testing.T) {
	target, stopper := runServer("localhost:0")
	defer stopper()

	ch1 := make(chan int, 1)
	ch2 := make(chan int, 1)

	go func() {
		defer close(ch1)
		defer close(ch2)
		client2 := connect(target)
		defer client2.Close()
		changes, err := client2.ServiceChanges()
		if err != nil {
			t.Fatalf("cannot get subscribe to changes: %s", err)
		}
		defer changes.Stop()

		change, err := changes.Read()
		if err != nil {
			t.Fatalf("changes returned: %v", err)
		}
		t.Logf("got change: %v", change)
		if change.TargetID != 1 || change.Name != "echo/myservice" {
			t.Fatalf("unexpected change received: %v", change)
		}

		stream, err := client2.(*client).Open(change.TargetID)
		if err != nil {
			t.Fatalf("failed to open: %v", err)
		}
		n, err := stream.Write([]byte{1, 2, 3, 4, 5, 6, 7, 8})
		if n != 8 || err != nil {
			t.Fatalf("failed to write: %d, %v", n, err)
		}
		var buf [8]byte
		n, err = stream.Read(buf[:])
		if n != 8 || err != copper.EINTERNAL {
			t.Fatalf("failed to read: %d, %v", n, err)
		}
		t.Logf("client read: % x", buf)
		stream.Close()

		ch1 <- 1

		change, err = changes.Read()
		if err != nil {
			t.Fatalf("changes returned: %v", err)
		}
		t.Logf("got change: %v", change)
		if change.TargetID != -1 || change.Name != "echo/myservice" {
			t.Fatalf("unexpected change2 received: %v", change)
		}

		ch2 <- 1
	}()

	client1 := connect(target)
	defer client1.Close()

	pub, err := client1.Publish("echo/myservice", PublishSettings{
		Distance:    2,
		Concurrency: 16,
	}, copper.StreamHandlerFunc(func(stream copper.Stream) {
		var buf [8]byte
		n, err := stream.Read(buf[:])
		if n != 8 || err != nil && err != io.EOF {
			t.Fatalf("myservice read: %d, %v", n, err)
		}
		t.Logf("myservice read: % x", buf[:n])
		stream.Write(buf[:n])
		stream.CloseWithError(copper.EINTERNAL)
	}))
	if err != nil {
		t.Fatalf("publication failed: %v", err)
	}

	if 1 != <-ch1 {
		return
	}

	pub.Stop()

	if 1 != <-ch2 {
		return
	}
}
