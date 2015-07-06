package copper

import (
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
	err = server.AddListener(listener)
	if err != nil {
		log.Fatalf("Failed to add listeners: %s", err)
	}
	go server.Serve()
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
	defer server.Shutdown()

	serverFinished := make(chan int, 1)

	go func() {
		defer close(serverFinished)
		conn := connectClient(addr)
		defer conn.Close()
		serverfunc(conn)
	}()

	func() {
		conn := connectClient(addr)
		defer conn.Close()
		clientfunc(conn)
	}()

	<-serverFinished
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
					Distance:    2,
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
				log.Fatalf("client: ServiceChanges: %s", err)
			}
			defer changes.Stop()

			changes1, err := changes.Read()
			if err != nil || !reflect.DeepEqual(changes1, expectedChanges1) {
				log.Fatalf("client: changes(1): %#v, %v", changes1, err)
			}

			unpublish <- 1

			changes2, err := changes.Read()
			if err != nil || !reflect.DeepEqual(changes2, expectedChanges2) {
				log.Fatalf("client: changes(2): %#v, %v", changes2, err)
			}
		},
		func(server Client) {
			defer close(published)

			pub, err := server.Publish(
				"test:myservice",
				PublishSettings{
					Priority:    1,
					Distance:    2,
					Concurrency: 3,
				},
				nil,
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
					Distance:    1,
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
					Distance:    2,
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
				log.Fatalf("client: ServiceChanges: %s", err)
			}
			defer changes.Stop()

			if 1 != <-published1 {
				return
			}

			changes1, err := changes.Read()
			if err != nil || !reflect.DeepEqual(changes1, expectedChanges1) {
				log.Fatalf("client: changes(1): %#v, %v", changes1, err)
			}

			publish2 <- 1
			if 1 != <-published2 {
				return
			}

			changes2, err := changes.Read()
			if err != nil || !reflect.DeepEqual(changes2, expectedChanges2) {
				log.Fatalf("client: changes(2): %#v, %v", changes2, err)
			}

			unpublish1 <- 1
			if 1 != <-unpublished1 {
				return
			}

			changes3, err := changes.Read()
			if err != nil || !reflect.DeepEqual(changes3, expectedChanges3) {
				log.Fatalf("client: changes(3): %#v, %v", changes3, err)
			}

			unpublish2 <- 1
			if 1 != <-unpublished2 {
				return
			}

			changes4, err := changes.Read()
			if err != nil || !reflect.DeepEqual(changes4, expectedChanges4) {
				log.Fatalf("client: changes(4): %#v, %v", changes4, err)
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
					Distance:    1,
					Concurrency: 2,
				},
				nil,
			)
			if err != nil {
				log.Fatalf("server: Publish(1): %s", err)
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
					Distance:    2,
					Concurrency: 3,
				},
				nil,
			)
			if err != nil {
				log.Fatalf("server: Publish(2): %s", err)
			}
			defer pub2.Stop()

			published2 <- 1
			if 1 != <-unpublish1 {
				return
			}

			err = pub1.Stop()
			if err != nil {
				log.Fatalf("server: Unpublish(1): %s", err)
			}

			unpublished1 <- 1
			if 1 != <-unpublish2 {
				return
			}

			err = pub2.Stop()
			if err != nil {
				log.Fatalf("server: Unpublish(2): %s", err)
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
					Distance:    1,
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
					Distance:    2,
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

func runClientServerService(clientfunc func(conn Client), serverfunc func(stream Stream), name string, settings PublishSettings) {
	published := make(chan int, 1)
	unpublish := make(chan int, 1)
	runClientServer(
		func(client Client) {
			defer close(unpublish)

			if 1 != <-published {
				return
			}

			clientfunc(client)

			unpublish <- 1
		},
		func(server Client) {
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
		func(stream Stream) {
			// nothing
		},
		"test:myservice",
		PublishSettings{
			Distance:    1,
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
		func(stream Stream) {
			stream.Write([]byte("hello"))
			stream.Peek()
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
		func(stream Stream) {
			stream.Write([]byte("hello"))
			stream.Peek()
		},
		"test:myservice",
		PublishSettings{
			Concurrency: 1,
			QueueSize:   1,
		},
	)
}

func runClientServerStream(clientfunc func(stream Stream), serverfunc func(stream Stream)) {
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

			clientfunc(stream)
		},
		serverfunc,
		"test:myservice",
		PublishSettings{
			Distance:    0,
			Concurrency: 1,
		},
	)
}

func TestClientServerStream(t *testing.T) {
	runClientServerStream(
		func(stream Stream) {
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
			stream.Close()
		},
		func(stream Stream) {
			var buf [8]byte
			n, err := stream.Read(buf[:])
			if n != 8 || err != nil {
				t.Fatalf("server read: %d, %v", n, err)
			}
			stream.Write(buf[:n])
			stream.CloseWithError(EINTERNAL)
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
		func(stream Stream) {
			defer close(mayReadResponse)
			defer close(mayCloseServer)

			_, err := stream.Peek()
			if err != nil {
				t.Fatalf("client: Peek: %v", err)
			}
			if 1 != <-mayCloseRead {
				return
			}
			stream.CloseReadError(EINTERNAL)

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
		func(stream Stream) {
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

			err = stream.WaitWriteClosed()
			if err != EINTERNAL {
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

func TestClientServerCloseReadBig(t *testing.T) {
	serverMayCloseRead := make(chan int, 1)
	serverMayClose := make(chan int, 1)

	runClientServerStream(
		func(client Stream) {
			defer close(serverMayCloseRead)
			defer close(serverMayClose)

			n, err := client.Write(make([]byte, 65536+16))
			if n != 65536+16 || err != nil {
				t.Fatalf("client: Write: %d, %v", n, err)
			}

			serverMayCloseRead <- 1

			n, err = client.WaitAck()
			if n != 16 || err != EINTERNAL {
				t.Fatalf("client: Write: %d, %v", n, err)
			}

			serverMayClose <- 1
		},
		func(server Stream) {
			buf, err := server.Peek()
			if len(buf) != 65536 || err != nil {
				t.Fatalf("server: Peek: %d, %v", len(buf), err)
			}

			if 1 != <-serverMayCloseRead {
				return
			}

			server.CloseReadError(EINTERNAL)

			<-serverMayClose
		},
	)
}

func TestClientServerPeers(t *testing.T) {
	server1, addr1 := runServer("localhost:0")
	defer server1.Shutdown()
	server2, addr2 := runServer("localhost:0")
	defer server2.Shutdown()
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
				Distance:    1,
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
				Distance:    0,
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
