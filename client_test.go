package copper

import (
	"io"
	"log"
	"net"
	"reflect"
	"testing"
	"time"
)

func runServer(address string) (Server, string) {
	server := NewServer()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Failed to listen: %s", err)
	}
	err = server.AddListener(listener, true)
	if err != nil {
		log.Fatalf("Failed to add listeners: %s", err)
	}
	return server, listener.Addr().String()
}

func connectClient(address string) Client {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		log.Fatalf("Failed to connect: %s", err)
	}
	return NewClient(conn)
}

func runClientServer(clientfunc func(conn Client), serverfunc func(conn Client)) {
	server, addr := runServer("localhost:0")
	defer server.Close()

	serverFinished := make(chan int, 1)
	clientFinished := make(chan int, 1)

	go func() {
		defer close(serverFinished)
		conn := connectClient(addr)
		defer conn.Close()
		serverfunc(conn)
	}()

	go func() {
		defer close(clientFinished)
		conn := connectClient(addr)
		defer conn.Close()
		clientfunc(conn)
	}()

	<-serverFinished
	<-clientFinished
}

func TestPublishChanges(t *testing.T) {
	published := make(chan int, 1)
	unpublish := make(chan int, 1)

	expectedChanges1 := ServiceChanges{
		Changed: []ServiceChange{
			{
				TargetID: 1,
				Name:     "test:myservice",
				Settings: PublishSettings{
					Priority:    1,
					MaxDistance: 2,
					Concurrency: 3,
				},
			},
		},
	}
	expectedChanges2 := ServiceChanges{
		Removed: []int64{1},
	}

	runClientServer(
		func(client Client) {
			defer close(unpublish)

			if 1 != <-published {
				return
			}

			changes, err := client.ServiceChanges()
			if err != nil {
				t.Fatalf("client: ServiceChanges: %s", err)
			}
			defer changes.Stop()

			changes1, err := changes.Read()
			if err != nil || !reflect.DeepEqual(changes1, expectedChanges1) {
				t.Fatalf("client: changes(1): %#v, %v", changes1, err)
			}

			unpublish <- 1

			changes2, err := changes.Read()
			if err != nil || !reflect.DeepEqual(changes2, expectedChanges2) {
				t.Fatalf("client: changes(2): %#v, %v", changes2, err)
			}
		},
		func(server Client) {
			defer close(published)

			pub, err := server.Publish(
				"test:myservice",
				PublishSettings{
					Priority:    1,
					MaxDistance: 2,
					Concurrency: 3,
				},
				nil,
			)
			if err != nil {
				t.Fatalf("server: Publish: %s", err)
			}
			defer pub.Stop()

			published <- 1
			if 1 != <-unpublish {
				return
			}

			err = pub.Stop()
			if err != nil {
				t.Fatalf("server: Unpublish: %s", err)
			}
		},
	)
}

func TestPublishPriorities(t *testing.T) {
	published1 := make(chan int, 1)
	publish2 := make(chan int, 1)
	published2 := make(chan int, 1)
	unpublish1 := make(chan int, 1)
	unpublished1 := make(chan int, 1)
	unpublish2 := make(chan int, 1)
	unpublished2 := make(chan int, 1)

	expectedChanges1 := ServiceChanges{
		Changed: []ServiceChange{
			{
				TargetID: 1,
				Name:     "test:myservice",
				Settings: PublishSettings{
					Priority:    0,
					MaxDistance: 1,
					Concurrency: 2,
				},
			},
		},
	}
	expectedChanges2 := ServiceChanges{
		Changed: []ServiceChange{
			{
				TargetID: 2,
				Name:     "test:myservice",
				Settings: PublishSettings{
					Priority:    1,
					MaxDistance: 2,
					Concurrency: 3,
				},
			},
		},
	}
	expectedChanges3 := ServiceChanges{
		Removed: []int64{1},
	}
	expectedChanges4 := ServiceChanges{
		Removed: []int64{2},
	}

	runClientServer(
		func(client Client) {
			defer close(publish2)
			defer close(unpublish1)
			defer close(unpublish2)

			changes, err := client.ServiceChanges()
			if err != nil {
				t.Fatalf("client: ServiceChanges: %s", err)
			}
			defer changes.Stop()

			if 1 != <-published1 {
				return
			}

			changes1, err := changes.Read()
			if err != nil || !reflect.DeepEqual(changes1, expectedChanges1) {
				t.Fatalf("client: changes(1): %#v, %v", changes1, err)
			}

			publish2 <- 1
			if 1 != <-published2 {
				return
			}

			changes2, err := changes.Read()
			if err != nil || !reflect.DeepEqual(changes2, expectedChanges2) {
				t.Fatalf("client: changes(2): %#v, %v", changes2, err)
			}

			unpublish1 <- 1
			if 1 != <-unpublished1 {
				return
			}

			changes3, err := changes.Read()
			if err != nil || !reflect.DeepEqual(changes3, expectedChanges3) {
				t.Fatalf("client: changes(3): %#v, %v", changes3, err)
			}

			unpublish2 <- 1
			if 1 != <-unpublished2 {
				return
			}

			changes4, err := changes.Read()
			if err != nil || !reflect.DeepEqual(changes4, expectedChanges4) {
				t.Fatalf("client: changes(4): %#v, %v", changes4, err)
			}
		},
		func(server Client) {
			defer close(published1)
			defer close(published2)
			defer close(unpublished1)
			defer close(unpublished2)

			pub1, err := server.Publish(
				"test:myservice",
				PublishSettings{
					Priority:    0,
					MaxDistance: 1,
					Concurrency: 2,
				},
				nil,
			)
			if err != nil {
				t.Fatalf("server: Publish(1): %s", err)
			}
			defer pub1.Stop()

			published1 <- 1
			if 1 != <-publish2 {
				return
			}

			pub2, err := server.Publish(
				"test:myservice",
				PublishSettings{
					Priority:    1,
					MaxDistance: 2,
					Concurrency: 3,
				},
				nil,
			)
			if err != nil {
				t.Fatalf("server: Publish(2): %s", err)
			}
			defer pub2.Stop()

			published2 <- 1
			if 1 != <-unpublish1 {
				return
			}

			err = pub1.Stop()
			if err != nil {
				t.Fatalf("server: Unpublish(1): %s", err)
			}

			unpublished1 <- 1
			if 1 != <-unpublish2 {
				return
			}

			err = pub2.Stop()
			if err != nil {
				t.Fatalf("server: Unpublish(2): %s", err)
			}

			unpublished2 <- 1
		},
	)
}

func TestSubscribePriorities(t *testing.T) {
	published := make(chan int, 1)
	unpublish1 := make(chan int, 1)
	unpublished1 := make(chan int, 1)
	unpublish2 := make(chan int, 1)
	unpublished2 := make(chan int, 1)

	runClientServer(
		func(client Client) {
			defer close(unpublish1)
			defer close(unpublish2)

			if 1 != <-published {
				return
			}

			sub, err := client.Subscribe(SubscribeSettings{
				Options: []SubscribeOption{
					{Service: "test:myservice"},
				},
			})
			if err != nil {
				t.Fatalf("client: Subscribe: %s", err)
			}
			defer sub.Stop()

			endpoints, err := sub.Endpoints()
			if err != nil || len(endpoints) != 1 || endpoints[0] != (Endpoint{TargetID: 1}) {
				t.Fatalf("client: Endpoints(1): %#v, %v", endpoints, err)
			}

			unpublish1 <- 1
			if 1 != <-unpublished1 {
				return
			}

			endpoints, err = sub.Endpoints()
			if err != nil || len(endpoints) != 1 || endpoints[0] != (Endpoint{TargetID: 2}) {
				t.Fatalf("client: Endpoints(2): %#v, %v", endpoints, err)
			}

			unpublish2 <- 1
			if 1 != <-unpublished2 {
				return
			}

			endpoints, err = sub.Endpoints()
			if err != nil || len(endpoints) != 0 {
				t.Fatalf("client: Endpoints(3): %#v, %v", endpoints, err)
			}
		},
		func(server Client) {
			defer close(published)
			defer close(unpublished1)
			defer close(unpublished2)

			pub1, err := server.Publish(
				"test:myservice",
				PublishSettings{
					Priority:    0,
					MaxDistance: 1,
					Concurrency: 2,
				},
				nil,
			)
			if err != nil {
				t.Fatalf("server: Publish(1): %s", err)
			}
			defer pub1.Stop()

			pub2, err := server.Publish(
				"test:myservice",
				PublishSettings{
					Priority:    1,
					MaxDistance: 2,
					Concurrency: 3,
				},
				nil,
			)
			if err != nil {
				t.Fatalf("server: Publish(2): %s", err)
			}
			defer pub2.Stop()

			published <- 1
			if 1 != <-unpublish1 {
				return
			}

			err = pub1.Stop()
			if err != nil {
				t.Fatalf("server: Unpublish(1): %s", err)
			}

			unpublished1 <- 1
			if 1 != <-unpublish2 {
				return
			}

			err = pub2.Stop()
			if err != nil {
				t.Fatalf("server: Unpublish(2): %s", err)
			}

			unpublished2 <- 1
		},
	)
}

func runClientServerService(clientfunc func(conn Client), serverfunc func(stream Stream) error, name string, settings PublishSettings) {
	published := make(chan int, 1)
	unpublish := make(chan int, 1)
	finished := make(chan int, 1)
	runClientServer(
		func(client Client) {
			defer close(unpublish)

			if 1 != <-published {
				return
			}

			clientfunc(client)

			unpublish <- 1
			<-finished
		},
		func(server Client) {
			defer close(finished)
			defer close(published)

			pub, err := server.Publish(
				name,
				settings,
				HandlerFunc(serverfunc),
			)
			if err != nil {
				log.Fatalf("server: Publish: %s", err)
			}
			defer pub.Stop()

			published <- 1
			if 1 != <-unpublish {
				return
			}

			err = pub.Stop()
			if err != nil {
				log.Fatalf("server: Unpublish: %s", err)
			}

			<-server.Shutdown()
			finished <- 1
		},
	)
}

func TestSubscribeEndpoints(t *testing.T) {
	expectedEndpoint := Endpoint{
		TargetID: 1,
	}
	runClientServerService(
		func(client Client) {
			sub1, err := client.Subscribe(SubscribeSettings{
				Options: []SubscribeOption{
					{Service: "test:myservice"},
				},
			})
			if err != nil {
				t.Fatalf("client: Subscribe(1): %s", err)
			}
			defer sub1.Stop()

			endpoints1, err := sub1.Endpoints()
			if err != nil || len(endpoints1) != 1 || endpoints1[0] != expectedEndpoint {
				t.Fatalf("client: Endpoints(1): %#v, %v", endpoints1, err)
			}

			sub2, err := client.Subscribe(SubscribeSettings{
				Options: []SubscribeOption{
					{Service: "test:myservice", MinDistance: 1, MaxDistance: 1},
				},
			})
			if err != nil {
				t.Fatalf("client: Subscribe(2): %s", err)
			}
			defer sub2.Stop()

			endpoints2, err := sub2.Endpoints()
			if err != nil || len(endpoints2) != 0 {
				t.Fatalf("client: Endpoints(2): %#v, %v", endpoints2, err)
			}
		},
		func(stream Stream) error {
			return nil
		},
		"test:myservice",
		PublishSettings{
			MaxDistance: 1,
			Concurrency: 2,
		},
	)
}

func TestSubscribeQueueFull(t *testing.T) {
	runClientServerService(
		func(client Client) {
			sub, err := client.Subscribe(SubscribeSettings{
				Options: []SubscribeOption{
					{Service: "test:myservice"},
				},
			})
			if err != nil {
				t.Fatalf("client: Subscribe: %s", err)
			}
			defer sub.Stop()

			stream1, err := sub.Open()
			if err != nil {
				t.Fatalf("client: Open(1): %s", err)
			}
			defer stream1.Close()
			n, err := stream1.Read(make([]byte, 16))
			if n != 5 || err != nil {
				t.Fatalf("client: Read(1): %d, %v", n, err)
			}

			stream2, err := sub.Open()
			if err != nil {
				t.Fatalf("client: Open(2): %s", err)
			}
			defer stream2.Close()
			n, err = stream2.Read(make([]byte, 16))
			if n != 0 || !isOverCapacity(err) {
				t.Fatalf("client: Read(2): %d, %v", n, err)
			}
		},
		func(stream Stream) error {
			stream.Write([]byte("hello"))
			stream.Peek()
			return nil
		},
		"test:myservice",
		PublishSettings{
			Concurrency: 1,
		},
	)
}

func TestSubscribeQueueWorks(t *testing.T) {
	runClientServerService(
		func(client Client) {
			sub, err := client.Subscribe(SubscribeSettings{
				Options: []SubscribeOption{
					{Service: "test:myservice"},
				},
			})
			if err != nil {
				t.Fatalf("client: Subscribe: %s", err)
			}
			defer sub.Stop()

			stream1, err := sub.Open()
			if err != nil {
				t.Fatalf("client: Open(1): %s", err)
			}
			defer stream1.Close()

			n, err := stream1.Read(make([]byte, 16))
			if n != 5 || err != nil {
				t.Fatalf("client: Read(1): %d, %v", n, err)
			}

			stream2, err := sub.Open()
			if err != nil {
				t.Fatalf("client: Open(2): %s", err)
			}
			defer stream2.Close()

			stream2.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
			n, err = stream2.Read(make([]byte, 16))
			if n != 0 || !isTimeout(err) {
				t.Fatalf("client: Read(2): %d, %v", n, err)
			}

			stream1.Close()
			stream2.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
			n, err = stream2.Read(make([]byte, 16))
			if n != 5 || err != nil {
				t.Fatalf("client: Read(3): %d, %v", n, err)
			}
		},
		func(stream Stream) error {
			stream.Write([]byte("hello"))
			stream.Peek()
			return nil
		},
		"test:myservice",
		PublishSettings{
			Concurrency: 1,
			QueueSize:   1,
		},
	)
}

func runClientServerStream(clientfunc func(stream Stream) error, serverfunc func(stream Stream) error) {
	runClientServerService(
		func(client Client) {
			sub, err := client.Subscribe(SubscribeSettings{
				Options: []SubscribeOption{
					{Service: "test:myservice"},
				},
			})
			if err != nil {
				log.Fatalf("client: Subscribe: %s", err)
			}
			defer sub.Stop()

			stream, err := sub.Open()
			if err != nil {
				log.Fatalf("client: Open: %s", err)
			}
			defer stream.Close()

			err = clientfunc(stream)
			if err != nil {
				stream.CloseWithError(err)
			}
		},
		serverfunc,
		"test:myservice",
		PublishSettings{
			MaxDistance: 0,
			Concurrency: 1,
		},
	)
}

func TestClientServerStream(t *testing.T) {
	runClientServerStream(
		func(stream Stream) error {
			n, err := stream.Write([]byte{1, 2, 3, 4, 5, 6, 7, 8})
			if n != 8 || err != nil {
				t.Fatalf("failed to write: %d, %v", n, err)
			}
			var buf [8]byte
			n, err = stream.Read(buf[:])
			if err == nil {
				_, err = stream.Peek()
			}
			if n != 8 || err != EINTERNAL {
				t.Fatalf("failed to read: %d, %v", n, err)
			}
			return nil
		},
		func(stream Stream) error {
			var buf [8]byte
			n, err := stream.Read(buf[:])
			if n != 8 || err != nil {
				t.Fatalf("server read: %d, %v", n, err)
			}
			stream.Write(buf[:n])
			return EINTERNAL
		},
	)
}

func TestClientServerNoRequest(t *testing.T) {
	serverMayClose := make(chan int, 1)
	runClientServerStream(
		func(client Stream) error {
			defer close(serverMayClose)
			n, err := client.Read(make([]byte, 16))
			if err == nil {
				_, err = client.Peek()
			}
			if n != 5 || err != io.EOF {
				t.Fatalf("client: Read: %d, %v", n, err)
			}
			if !client.IsAcknowledged() {
				t.Fatalf("client: not acknowledged!")
			}
			serverMayClose <- 1
			return nil
		},
		func(server Stream) error {
			n, err := server.Write([]byte("hello"))
			if n != 5 || err != nil {
				t.Fatalf("server: Write: %d, %v", n, err)
			}
			server.CloseWrite()
			<-serverMayClose
			return nil
		},
	)
}

func TestClientServerBigRequest(t *testing.T) {
	runClientServerStream(
		func(client Stream) error {
			// N.B. need to send more than 65536*3, here's why:
			// - first 64k will be sent and peeked in passthru
			// - there will be a window update equal to those 64k
			// - stream should get acknowledged, however it may happen after
			//   earlier window update and it will become pending
			// - second 64k will be sent on the wire
			// - before we receive a window update third 64k might get buffered
			//   locally, and the Write() call might return before receiving
			//   an ack frame
			// - to make sure we receive an ack we need to either flush or send
			//   more data, then ack will be sent before the new window update
			//   and we should receive it reliably
			buf := make([]byte, 65536)
			for i := 0; i < 4; i++ {
				_, err := client.Write(buf)
				if err != nil {
					t.Fatalf("client: Write: %s", err)
				}
			}
			if !client.IsAcknowledged() {
				t.Fatalf("client: not acknowledged!")
			}
			return nil
		},
		func(server Stream) error {
			buf := make([]byte, 4096)
			total := 0
			for {
				n, err := server.Read(buf)
				total += n
				if err != nil {
					if err == io.EOF {
						break
					}
					t.Fatalf("server: Read: %s (total %d)", err, total)
				}
			}
			if total != 65536*4 {
				t.Fatalf("server: total received: %d", total)
			}
			return nil
		},
	)
}

func TestClientServerCloseRead(t *testing.T) {
	mayCloseRead := make(chan int, 1)
	mayWriteResponse := make(chan int, 1)
	mayReadResponse := make(chan int, 1)
	mayCloseClient := make(chan int, 1)
	mayCloseServer := make(chan int, 1)
	runClientServerStream(
		func(stream Stream) error {
			defer close(mayReadResponse)
			defer close(mayCloseServer)

			_, err := stream.Peek()
			if err != nil {
				t.Fatalf("client: Peek: %v", err)
			}
			if 1 != <-mayCloseRead {
				return nil
			}
			stream.CloseReadWithError(EINTERNAL)

			if 1 != <-mayWriteResponse {
				return nil
			}
			n, err := stream.Write([]byte{1, 2, 3, 4})
			if n != 4 || err != nil {
				t.Fatalf("client: Write: %d, %v", n, err)
			}
			mayReadResponse <- 1

			<-mayCloseClient
			mayCloseServer <- 1
			return nil
		},
		func(stream Stream) error {
			defer close(mayCloseRead)
			defer close(mayWriteResponse)
			defer close(mayCloseClient)

			n, err := stream.Write([]byte{5, 6, 7, 8})
			if n != 4 || err != nil {
				t.Fatalf("server: Write: %d, %v", n, err)
			}
			err = stream.Flush()
			if err != nil {
				t.Fatalf("server: Flush: %v", err)
			}
			mayCloseRead <- 1

			<-stream.WriteClosed()
			err = stream.WriteErr()
			if err != EINTERNAL {
				t.Fatalf("server: WaitWriteClosed: %v", err)
			}

			mayWriteResponse <- 1
			if 1 != <-mayReadResponse {
				return nil
			}
			n, err = stream.Read(make([]byte, 16))
			if n != 4 || err != nil {
				t.Fatalf("server: Read: %d, %v", n, err)
			}
			mayCloseClient <- 1
			<-mayCloseServer
			return nil
		},
	)
}

func TestClientServerCloseReadBig(t *testing.T) {
	serverMayCloseRead := make(chan int, 1)
	serverMayClose := make(chan int, 1)

	runClientServerStream(
		func(client Stream) error {
			defer close(serverMayCloseRead)
			defer close(serverMayClose)

			n, err := client.Write(make([]byte, 65536+16))
			if n != 65536+16 || err != nil {
				t.Fatalf("client: Write: %d, %v", n, err)
			}

			serverMayCloseRead <- 1

			<-client.WriteClosed()
			if client.WriteErr() != EINTERNAL {
				t.Fatalf("client: WriteErr: %v", client.WriteErr())
			}

			serverMayClose <- 1
			return nil
		},
		func(server Stream) error {
			buf, err := server.Peek()
			if len(buf) < 65536-128 || err != nil {
				t.Fatalf("server: Peek: %d, %v", len(buf), err)
			}

			if 1 != <-serverMayCloseRead {
				return nil
			}

			server.CloseReadWithError(EINTERNAL)

			<-serverMayClose
			return nil
		},
	)
}

func TestClientServerPeers(t *testing.T) {
	server1, addr1 := runServer("localhost:0")
	defer server1.Close()
	server2, addr2 := runServer("localhost:0")
	defer server2.Close()
	server2.AddPeer("tcp", addr1, 1)

	published := make(chan int, 1)
	serverMayClose := make(chan int, 1)

	go func() {
		defer close(published)
		client := connectClient(addr1)
		defer client.Close()

		pub1, err := client.Publish(
			"test:myservice1",
			PublishSettings{
				Concurrency: 1,
				MaxDistance: 1,
			},
			nil,
		)
		if err != nil {
			t.Fatalf("server1: Publish(1): %s", err)
		}
		defer pub1.Stop()

		pub2, err := client.Publish(
			"test:myservice2",
			PublishSettings{
				Concurrency: 1,
				MaxDistance: 0,
			},
			nil,
		)
		if err != nil {
			t.Fatalf("server1: Publish(2): %s", err)
		}
		defer pub2.Stop()

		published <- 1
		<-serverMayClose
	}()

	expectedEndpoints1 := []Endpoint{
		{Network: "tcp", Address: addr1, TargetID: 1},
	}
	expectedEndpoints2 := []Endpoint(nil)

	func() {
		defer close(serverMayClose)
		client := connectClient(addr2)

		if 1 != <-published {
			return
		}
		// TODO: need to synchronize properly! The challenge is to wait for
		// publication changes to reach the second server.
		time.Sleep(5 * time.Millisecond)

		sub1, err := client.Subscribe(SubscribeSettings{
			Options: []SubscribeOption{
				{Service: "test:myservice1", MaxDistance: 1},
			},
		})
		if err != nil {
			t.Fatalf("server2: Subscribe(1): %s", err)
		}
		defer sub1.Stop()

		endpoints1, err := sub1.Endpoints()
		if err != nil || !reflect.DeepEqual(endpoints1, expectedEndpoints1) {
			t.Fatalf("server2: Endpoints(1): %#v, %s (expected %#v)", endpoints1, err, expectedEndpoints1)
		}

		sub2, err := client.Subscribe(SubscribeSettings{
			Options: []SubscribeOption{
				{Service: "test:myservice2", MaxDistance: 1},
			},
		})
		if err != nil {
			t.Fatalf("server2: Subscribe(2): %s", err)
		}
		defer sub2.Stop()

		endpoints2, err := sub2.Endpoints()
		if err != nil || !reflect.DeepEqual(endpoints2, expectedEndpoints2) {
			t.Fatalf("server2: Endpoints(2): %#v, %s (expected %#v)", endpoints2, err, expectedEndpoints2)
		}

		serverMayClose <- 1
	}()
}
