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

func runClientServerRaw(clientfunc func(conn Client), serverfunc func(conn Client)) {
	target, stopper := runServer("localhost:0")
	defer stopper()

	serverFinished := make(chan int, 1)

	go func() {
		defer close(serverFinished)
		conn := connect(target)
		defer conn.Close()
		serverfunc(conn)
	}()

	func() {
		conn := connect(target)
		defer conn.Close()
		clientfunc(conn)
	}()

	<-serverFinished
}

func TestPublishChanges(t *testing.T) {
	published := make(chan int, 1)
	unpublish := make(chan int, 1)

	expectedChange1 := ServiceChange{
		TargetID: 1,
		Name:     "test:myservice",
		Settings: PublishSettings{
			Priority:    1,
			Distance:    2,
			Concurrency: 3,
		},
		Valid: true,
	}
	expectedChange2 := ServiceChange{
		TargetID: 1,
		Name:     "test:myservice",
		Settings: PublishSettings{
			Priority: 1,
		},
		Valid: false,
	}

	runClientServerRaw(
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

			change, err := changes.Read()
			if err != nil || change != expectedChange1 {
				log.Fatalf("client: changes(1): %#v, %v", change, err)
			}

			unpublish <- 1

			change, err = changes.Read()
			if err != nil || change != expectedChange2 {
				log.Fatalf("client: changes(2): %#v, %v", change, err)
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

	expectedChange1 := ServiceChange{
		TargetID: 1,
		Name:     "test:myservice",
		Settings: PublishSettings{
			Priority:    0,
			Distance:    1,
			Concurrency: 2,
		},
		Valid: true,
	}
	expectedChange2 := ServiceChange{
		TargetID: 2,
		Name:     "test:myservice",
		Settings: PublishSettings{
			Priority:    1,
			Distance:    2,
			Concurrency: 3,
		},
		Valid: true,
	}
	expectedChange3 := ServiceChange{
		TargetID: 1,
		Name:     "test:myservice",
		Settings: PublishSettings{
			Priority: 0,
		},
		Valid: false,
	}
	expectedChange4 := ServiceChange{
		TargetID: 2,
		Name:     "test:myservice",
		Settings: PublishSettings{
			Priority: 1,
		},
		Valid: false,
	}

	runClientServerRaw(
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

			change, err := changes.Read()
			if err != nil || change != expectedChange1 {
				log.Fatalf("client: changes(1): %#v, %v", change, err)
			}

			publish2 <- 1
			if 1 != <-published2 {
				return
			}

			change, err = changes.Read()
			if err != nil || change != expectedChange2 {
				log.Fatalf("client: changes(2): %#v, %v", change, err)
			}

			unpublish1 <- 1
			if 1 != <-unpublished1 {
				return
			}

			change, err = changes.Read()
			if err != nil || change != expectedChange3 {
				log.Fatalf("client: changes(3): %#v, %v", change, err)
			}

			unpublish2 <- 1
			if 1 != <-unpublished2 {
				return
			}

			change, err = changes.Read()
			if err != nil || change != expectedChange4 {
				log.Fatalf("client: changes(4): %#v, %v", change, err)
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

	runClientServerRaw(
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

func runClientServerService(clientfunc func(conn Client), serverfunc func(stream copper.Stream), name string, settings PublishSettings) {
	published := make(chan int, 1)
	unpublish := make(chan int, 1)
	runClientServerRaw(
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
				copper.StreamHandlerFunc(serverfunc),
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
		func(stream copper.Stream) {
			// nothing
		},
		"test:myservice",
		PublishSettings{
			Distance:    1,
			Concurrency: 2,
		},
	)
}

func runClientServerStream(clientfunc func(stream copper.Stream), serverfunc func(stream copper.Stream)) {
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
			stream.Close()
		},
		func(stream copper.Stream) {
			var buf [8]byte
			n, err := stream.Read(buf[:])
			if n != 8 || err != nil {
				t.Fatalf("server read: %d, %v", n, err)
			}
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
	runClientServerStream(
		func(stream copper.Stream) {
			defer close(mayReadResponse)
			defer close(mayCloseServer)

			_, err := stream.Peek()
			if err != nil {
				t.Fatalf("client: Peek: %v", err)
			}
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
			err = stream.Flush()
			if err != nil {
				t.Fatalf("server: Flush: %v", err)
			}
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
