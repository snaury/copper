package copperd

import (
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

func runClientServerConn(clientfunc func(conn copper.Conn), serverfunc func(stream copper.Stream)) {
	target, stopper := runServer("localhost:0")
	defer stopper()

	clientFinished := make(chan int, 1)
	unpublishSeen := make(chan int, 1)

	go func() {
		defer close(clientFinished)
		defer close(unpublishSeen)

		c := connect(target)
		defer c.Close()

		changes, err := c.ServiceChanges()
		if err != nil {
			log.Fatalf("c.ServiceChanges: %s", err)
		}
		defer changes.Stop()

		change, err := changes.Read()
		if err != nil || change.TargetID != 1 || change.Name != "test/myservice" {
			log.Fatalf("changes.Read(1): %#v, %v", change, err)
		}

		clientfunc(c.(*clientConn))

		clientFinished <- 1

		change, err = changes.Read()
		if err != nil || change.TargetID != -1 || change.Name != "test/myservice" {
			log.Fatalf("changes.Read(2): %#v, %v", change, err)
		}

		unpublishSeen <- 1
	}()

	func() {
		c := connect(target)
		defer c.Close()

		pub, err := c.Publish("test/myservice", PublishSettings{
			Distance:    2,
			Concurrency: 3,
		}, copper.StreamHandlerFunc(func(stream copper.Stream) {
			serverfunc(stream)
		}))
		if err != nil {
			log.Fatalf("c.Publish: %v", err)
		}
		defer pub.Stop()

		<-clientFinished
	}()

	<-unpublishSeen
}

func runClientServer(clientfunc func(stream copper.Stream), serverfunc func(stream copper.Stream)) {
	runClientServerConn(
		func(conn copper.Conn) {
			stream, err := conn.Open(1)
			if err != nil {
				log.Fatalf("conn.Open: %v", err)
			}
			defer stream.Close()
			clientfunc(stream)
		},
		serverfunc,
	)
}

func TestClientServer(t *testing.T) {
	runClientServer(
		func(stream copper.Stream) {
			n, err := stream.Write([]byte{1, 2, 3, 4, 5, 6, 7, 8})
			if n != 8 || err != nil {
				t.Fatalf("failed to write: %d, %v", n, err)
			}
			var buf [8]byte
			n, err = stream.Read(buf[:])
			if err == nil {
				_, err = stream.Peek()
			}
			if n != 8 || err != copper.EINTERNAL {
				t.Fatalf("failed to read: %d, %v", n, err)
			}
			t.Logf("client read: % x", buf)
			stream.Close()
		},
		func(stream copper.Stream) {
			var buf [8]byte
			n, err := stream.Read(buf[:])
			if n != 8 || err != nil {
				t.Fatalf("server read: %d, %v", n, err)
			}
			t.Logf("server read: % x", buf[:n])
			stream.Write(buf[:n])
			stream.CloseWithError(copper.EINTERNAL)
		},
	)
}

func TestClientServerCloseRead(t *testing.T) {
	mayCloseRead := make(chan int, 1)
	mayWriteResponse := make(chan int, 1)
	mayReadResponse := make(chan int, 1)
	mayCloseClient := make(chan int, 1)
	mayCloseServer := make(chan int, 1)
	runClientServer(
		func(stream copper.Stream) {
			defer close(mayReadResponse)
			defer close(mayCloseServer)

			if 1 != <-mayCloseRead {
				return
			}
			stream.CloseReadError(copper.EINTERNAL)

			if 1 != <-mayWriteResponse {
				return
			}
			n, err := stream.Write([]byte{1, 2, 3, 4})
			if n != 4 || err != nil {
				t.Fatalf("client: Write: %d, %v", n, err)
			}
			mayReadResponse <- 1

			n, err = stream.WaitAck()
			if n != 0 || err != nil {
				t.Fatalf("client: WaitAck: %d, %v", n, err)
			}
			<-mayCloseClient
			mayCloseServer <- 1
		},
		func(stream copper.Stream) {
			defer close(mayCloseRead)
			defer close(mayWriteResponse)
			defer close(mayCloseClient)

			n, err := stream.Write([]byte{5, 6, 7, 8})
			if n != 4 || err != nil {
				t.Fatalf("server: Write: %d, %v", n, err)
			}
			stream.Flush()
			mayCloseRead <- 1

			err = stream.WaitWriteClosed()
			if err != copper.EINTERNAL {
				t.Fatalf("server: WaitWriteClosed: %v", err)
			}

			mayWriteResponse <- 1
			if 1 != <-mayReadResponse {
				return
			}
			n, err = stream.Read(make([]byte, 16))
			if n != 4 || err != nil {
				t.Fatalf("server: Read: %d, %v", n, err)
			}
			mayCloseClient <- 1
			<-mayCloseServer
		},
	)
}
