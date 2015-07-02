package copper

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func toHandler(handler func(stream Stream)) StreamHandler {
	if handler != nil {
		return StreamHandlerFunc(handler)
	}
	return nil
}

func runClientServerStream(clientfunc func(client Stream), serverfunc func(stream Stream)) {
	closeClient := make(chan int, 1)
	rawserver, rawclient := net.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		client := NewConn(rawclient, nil, false)
		defer client.Close()
		func() {
			stream, err := client.Open(0)
			if err != nil {
				panic(fmt.Sprintf("client.Open: %s", err))
			}
			defer stream.Close()
			clientfunc(stream)
		}()
		<-closeClient
	}()
	go func() {
		defer wg.Done()
		handler := StreamHandlerFunc(func(stream Stream) {
			defer close(closeClient)
			serverfunc(stream)
		})
		server := NewConn(rawserver, toHandler(handler), true)
		defer server.Close()
		server.Wait()
	}()
	wg.Wait()
}

func runClientServerHandler(clientfunc func(client Conn), handler func(stream Stream)) {
	rawserver, rawclient := net.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		client := NewConn(rawclient, nil, false)
		defer client.Close()
		clientfunc(client)
	}()
	go func() {
		defer wg.Done()
		server := NewConn(rawserver, toHandler(handler), true)
		defer server.Close()
		server.Wait()
	}()
	wg.Wait()
}

func TestConnStreams(t *testing.T) {
	serverReady := make(chan int, 1)

	var wg sync.WaitGroup
	serverconn, clientconn := net.Pipe()
	wg.Add(2)
	go func() {
		defer close(serverReady)
		var err error
		defer wg.Done()
		closeErrors := map[int64]error{
			1: nil,
			2: ENOROUTE,
			3: ENOTARGET,
		}
		handler := StreamHandlerFunc(func(stream Stream) {
			r := bufio.NewReader(stream)
			line, err := r.ReadString('\n')
			if err != io.EOF {
				t.Fatalf("handler: ReadString: expected io.EOF, got %#v", err)
			}
			if stream.(*rawStream).flags&flagStreamSeenEOF == 0 {
				t.Fatalf("handler: stream did not see EOF yet")
			}
			_, err = fmt.Fprintf(stream, "%d: '%s'", stream.TargetID(), line)
			if err != nil {
				t.Fatalf("handler: Fprintf: unexpected error: %v", err)
			}
			// Common sense dictates, that data from Fprintf should reach
			// the other side when we close the stream!
			stream.CloseWithError(closeErrors[stream.TargetID()])
		})
		hmap := NewStreamHandlerMap(nil)
		hmap.Add(handler)
		hmap.Add(handler)
		hmap.Add(handler)
		server := NewConn(serverconn, hmap, true)
		defer server.Close()

		stream, err := server.Open(51)
		if err != nil {
			t.Fatalf("server: Open: unexpected error: %v", err)
		}
		_, err = stream.Read(make([]byte, 256))
		if err != ENOTARGET {
			t.Fatalf("server: Read: expected ENOTARGET, got: %v", err)
		}
		serverReady <- 1

		err = server.Wait()
		if err != ECONNCLOSED {
			t.Fatalf("server: Wait: expected ECONNCLOSED, got: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		client := NewConn(clientconn, nil, false)
		defer client.Close()
		if 1 != <-serverReady {
			return
		}

		messages := map[int64]string{
			0: "foo",
			1: "hello",
			2: "world stuff",
			3: "some unexpected message",
			4: "not registered yet",
		}
		expectedError := map[int64]error{
			0: ENOTARGET,
			1: io.EOF,
			2: ENOROUTE,
			3: ENOTARGET,
			4: ENOTARGET,
		}
		expectedResponse := map[int64]string{
			0: "",
			1: "1: 'hello'",
			2: "2: 'world stuff'",
			3: "3: 'some unexpected message'",
			4: "",
		}
		var wgnested sync.WaitGroup
		for target := range messages {
			wgnested.Add(1)
			go func(target int64) {
				defer wgnested.Done()

				client.(*rawConn).blockWrite()
				stream, err := client.Open(target)
				if err != nil {
					t.Fatalf("client: Open(%d): unexpected error: %v", target, err)
				}
				defer stream.Close()
				_, err = stream.Write([]byte(messages[target]))
				if err != nil {
					t.Fatalf("client: Write(%d): unexpected error: %v", target, err)
				}
				err = stream.CloseWrite()
				if err != nil {
					t.Fatalf("client: CloseWrite(%d): unexpected error: %v", target, err)
				}
				client.(*rawConn).unblockWrite()

				r := bufio.NewReader(stream)
				line, err := r.ReadString('\n')
				if err != expectedError[target] {
					t.Fatalf("client: ReadString(%d): expected %v, got: %v", target, expectedError[target], err)
				}
				if line != expectedResponse[target] {
					t.Fatalf("client: ReadString(%d): unexpected response: %q", target, line)
				}
			}(target)
		}
		wgnested.Wait()
	}()
	wg.Wait()
}

// TODO: remove and switch to new functions
func runClientServer(handler StreamHandler, clientfunc func(client Conn)) {
	rawserver, rawclient := net.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		server := NewConn(rawserver, handler, true)
		defer server.Close()
		server.Wait()
	}()
	go func() {
		defer wg.Done()
		client := NewConn(rawclient, nil, false)
		defer client.Close()
		clientfunc(client)
	}()
	wg.Wait()
}

func TestConnPing(t *testing.T) {
	runClientServer(nil, func(client Conn) {
		var ch1, ch2 <-chan error
		// simple ping
		if err := <-client.Ping(123); err != nil {
			t.Fatalf("client: Ping: unexpected error: %v", err)
		}
		// two simultaneous pings, same id
		ch1 = client.Ping(42)
		ch2 = client.Ping(42)
		if err := <-ch1; err != nil {
			t.Fatalf("client: Ping: unexpected error: %v", err)
		}
		if err := <-ch2; err != nil {
			t.Fatalf("client: Ping: unexpected error: %v", err)
		}
		// two simultaneous pings that fail before getting response
		client.(*rawConn).blockWrite()
		ch1 = client.Ping(51)
		ch2 = client.Ping(51)
		client.Close()
		client.(*rawConn).unblockWrite()
		if err := <-ch1; err != ECONNCLOSED {
			t.Fatalf("client: Ping: expected ECONNCLOSED, not %v", err)
		}
		if err := <-ch2; err != ECONNCLOSED {
			t.Fatalf("client: Ping: expected ECONNCLOSED, not %v", err)
		}
		// simple ping on closed connection should immediately fail
		if err := <-client.Ping(321); err != ECONNCLOSED {
			t.Fatalf("client: Ping: expected ECONNCLOSED, not %v", err)
		}
	})
}

func TestConnSync(t *testing.T) {
	runClientServer(StreamHandlerFunc(func(stream Stream) {
		stream.Read(make([]byte, 16))
		stream.CloseWithError(ENOROUTE)
	}), func(client Conn) {
		stream, err := client.Open(42)
		if err != nil {
			t.Fatalf("client: Open: %s", err)
		}
		defer stream.Close()
		if stream.StreamID() != 1 {
			t.Fatalf("client: unexpected stream id: %d (expected 1)", stream.StreamID())
		}
		stream.CloseWrite()
		_, err = stream.Read(make([]byte, 256))
		if err != ENOROUTE {
			t.Fatalf("client: Read: %s (expected ENOROUTE)", err)
		}
		stream.Close()

		err = <-client.Sync()
		if err != nil {
			t.Fatalf("client: Sync: %s", err)
		}

		stream, err = client.Open(43)
		if err != nil {
			t.Fatalf("client: Open: %s", err)
		}
		defer stream.Close()
		if stream.StreamID() != 1 {
			t.Fatalf("client: unexpected stream id: %d (expected to reuse 1)", stream.StreamID())
		}
	})
}

func TestStreamBigWrite(t *testing.T) {
	runClientServer(StreamHandlerFunc(func(stream Stream) {
		stream.Peek()
		stream.CloseWithError(ENOROUTE)
	}), func(client Conn) {
		stream, err := client.Open(0)
		if err != nil {
			t.Fatalf("client: Open: %s", err)
		}
		defer stream.Close()
		n, err := stream.Write(make([]byte, 65536+16))
		if n != 65536 || err != ENOROUTE {
			t.Fatalf("client: Write: unexpected result: %d, %v", n, err)
		}
	})
}

func isTimeout(err error) bool {
	if e, ok := err.(net.Error); ok {
		return e.Timeout()
	}
	return false
}

func TestStreamReadDeadline(t *testing.T) {
	runClientServer(StreamHandlerFunc(func(stream Stream) {
		stream.Read(make([]byte, 256))
	}), func(client Conn) {
		var deadline time.Time
		stream, err := client.Open(0)
		if err != nil {
			t.Fatalf("client: Open: %s", err)
		}
		defer stream.Close()

		deadline = time.Now().Add(5 * time.Millisecond)
		stream.SetReadDeadline(deadline)
		n, err := stream.Read(make([]byte, 256))
		if n != 0 || !isTimeout(err) {
			t.Fatalf("client: Read: expected timeout, got: %d, %v", n, err)
		}
		if deadline.After(time.Now()) {
			t.Fatalf("client: Read: expected to timeout after the deadline, not before")
		}
	})
}

func TestStreamWriteDeadline(t *testing.T) {
	runClientServer(StreamHandlerFunc(func(stream Stream) {
		stream.Write(make([]byte, 65536+16))
	}), func(client Conn) {
		var deadline time.Time
		stream, err := client.Open(0)
		if err != nil {
			t.Fatalf("client: Open: %s", err)
		}
		defer stream.Close()

		deadline = time.Now().Add(5 * time.Millisecond)
		stream.SetWriteDeadline(deadline)
		n, err := stream.Write(make([]byte, 65536+16))
		if n != 65536 || !isTimeout(err) {
			t.Fatalf("client: Write: expected timeout, got: %d, %v", n, err)
		}
		if deadline.After(time.Now()) {
			t.Fatalf("client: Write: expected to timeout after the deadline, not before")
		}

		deadline = time.Now().Add(5 * time.Millisecond)
		stream.SetWriteDeadline(deadline)
		n, err = stream.WaitAck()
		if n != 65536 || !isTimeout(err) {
			t.Fatalf("client: WaitAck: expected timeout, got: %d, %v", n, err)
		}
		if deadline.After(time.Now()) {
			t.Fatalf("client: WaitAck: expected to timeout after the deadline, not before")
		}
	})
}

func TestStreamWaitAck(t *testing.T) {
	var lock sync.Mutex
	var dataseen bool
	var waitdone sync.Cond
	waitdone.L = &lock
	var wg sync.WaitGroup
	runClientServer(StreamHandlerFunc(func(stream Stream) {
		defer wg.Done()
		switch stream.TargetID() {
		case 1:
			// 1st case: close the connection
			stream.Peek()
			lock.Lock()
			defer lock.Unlock()
			dataseen = true
			err := stream.Close()
			if err != nil {
				t.Fatalf("server: Close: %s", err)
			}
		case 2:
			// 2nd case: close with ENOROUTE
			stream.Peek()
			lock.Lock()
			defer lock.Unlock()
			dataseen = true
			err := stream.CloseWithError(ENOROUTE)
			if err != nil {
				t.Fatalf("server: CloseWithError: %s", err)
			}
		case 3:
			// 3rd case: read data and sleep a little
			stream.Peek()
			lock.Lock()
			defer lock.Unlock()
			_, err := stream.Read(make([]byte, 16))
			if err != nil {
				t.Fatalf("server: Read: %s", err)
			}
			dataseen = true
			waitdone.Wait()
			err = stream.Close()
			if err != nil {
				t.Fatalf("server: Close: %s", err)
			}
		default:
			err := stream.CloseWithError(ENOTARGET)
			if err != nil {
				t.Fatalf("server: CloseWithError: %s", err)
			}
		}
	}), func(client Conn) {
		for target := int64(1); target <= 3; target++ {
			wg.Add(1)
			stream, err := client.Open(target)
			if err != nil {
				t.Fatalf("client: Open: %s", err)
			}
			defer stream.Close()
			_, err = stream.Write([]byte("Hello, world!"))
			if err != nil {
				t.Fatalf("client: Write: %s", err)
			}
			n, err := stream.WaitAck()
			lock.Lock()
			seen := dataseen
			dataseen = false
			waitdone.Broadcast()
			lock.Unlock()
			switch target {
			case 1:
				if n != 13 || err != ESTREAMCLOSED {
					t.Fatalf("client: WaitAck(%d): %d, %v (expected 13, ESTREAMCLOSED)", target, n, err)
				}
			case 2:
				if n != 13 || err != ENOROUTE {
					t.Fatalf("client: WaitAck(%d): %d, %v (expected 13, ETARGET)", target, n, err)
				}
			default:
				if n != 0 || err != nil {
					t.Fatalf("client: WaitAck(%d): %d, %v (expected <nil>)", target, n, err)
				}
			}
			if !seen {
				t.Fatalf("client: WaitAck(%d): data was not confirmed before the call returned", target)
			}
		}
		wg.Wait() // wait until server finishes
	})
}

func TestStreamCloseRead(t *testing.T) {
	var goahead1 sync.Mutex
	var goahead2 sync.Mutex
	goahead1.Lock()
	goahead2.Lock()
	runClientServer(StreamHandlerFunc(func(stream Stream) {
		_, err := stream.Peek()
		if err != nil {
			t.Fatalf("server: Peek: %v", err)
		}
		err = stream.CloseRead()
		if err != nil {
			t.Fatalf("server: CloseRead: %v", err)
		}
		goahead1.Lock()
		n, err := stream.Write([]byte("hello"))
		if n != 5 || err != nil {
			t.Fatalf("server: Write: %d, %v", n, err)
		}
		n, err = stream.WaitAck()
		if n != 0 || err != nil {
			t.Fatalf("server: WaitAck: %d, %v", n, err)
		}
		goahead2.Unlock()
	}), func(client Conn) {
		stream, err := client.Open(0)
		if err != nil {
			t.Fatalf("client: Open: %v", err)
		}
		defer stream.Close()

		stream.Write([]byte("foobar"))
		n, err := stream.WaitAck()
		goahead1.Unlock()
		if n != 6 || err != ESTREAMCLOSED {
			t.Fatalf("client: WaitAck: %d, %v", n, err)
		}
		n, err = stream.Read(make([]byte, 16))
		if n != 5 || err != nil {
			t.Fatalf("client: Read: %d, %v", n, err)
		}
		goahead2.Lock()
	})
}

func TestStreamCloseWithData(t *testing.T) {
	var goahead1 sync.Mutex
	var goahead2 sync.Mutex
	goahead1.Lock()
	goahead2.Lock()
	runClientServer(StreamHandlerFunc(func(stream Stream) {
		goahead1.Unlock()
		defer goahead2.Unlock()
		n, err := stream.Read(make([]byte, 16))
		if err == nil {
			// with GOMAXPROCS>1 we may see data before we see error, make sure
			// to wait for the error as well.
			_, err = stream.Peek()
		}
		if n != 5 || err != ENOROUTE {
			t.Fatalf("server: Read: %d, %v", n, err)
		}
	}), func(client Conn) {
		stream, err := client.Open(0)
		if err != nil {
			t.Fatalf("server: Open: %v", err)
		}
		defer stream.Close()

		goahead1.Lock()
		client.(*rawConn).blockWrite()
		n, err := stream.Write([]byte("hello"))
		if n != 5 || err != nil {
			t.Fatalf("client: Write: %d, %v", n, err)
		}
		stream.CloseWithError(ENOROUTE)
		client.(*rawConn).unblockWrite()
		goahead2.Lock()
	})
}

func TestStreamCloseAfterData(t *testing.T) {
	var goahead1 sync.Mutex
	var goahead2 sync.Mutex
	var goahead3 sync.Mutex
	goahead1.Lock()
	goahead2.Lock()
	goahead3.Lock()
	runClientServer(StreamHandlerFunc(func(stream Stream) {
		goahead1.Unlock()
		n, err := stream.Read(make([]byte, 16))
		goahead2.Unlock()
		defer goahead3.Unlock()
		if n != 5 || err != nil {
			t.Fatalf("server: Read: %d, %v", n, err)
		}
		_, err = stream.Peek()
		if err != ENOROUTE {
			t.Fatalf("server Peek: %v", err)
		}
	}), func(client Conn) {
		stream, err := client.Open(0)
		if err != nil {
			t.Fatalf("server: Open: %v", err)
		}
		defer stream.Close()

		goahead1.Lock()
		n, err := stream.Write([]byte("hello"))
		if n != 5 || err != nil {
			t.Fatalf("client: Write: %d, %v", n, err)
		}
		goahead2.Lock()
		err = stream.CloseWithError(ENOROUTE)
		if err != nil {
			t.Fatalf("client: CloseWithError: %v", err)
		}
		goahead3.Lock()
	})
}

func TestConnClose(t *testing.T) {
	runClientServer(StreamHandlerFunc(func(stream Stream) {
		n, err := stream.Write([]byte("Hello, world!"))
		if n != 13 || err != nil {
			t.Fatalf("server: Write: %d, %v", n, err)
		}
		n, err = stream.WaitAck()
		if n != 5 || err != ECONNCLOSED {
			t.Fatalf("server: WaitAck: %d, %v", n, err)
		}
	}), func(client Conn) {
		stream, err := client.Open(0)
		if err != nil {
			t.Fatalf("client: Open: %v", err)
		}
		defer stream.Close()

		_, err = stream.Peek()
		if err != nil {
			t.Fatalf("client: Peek: %v", err)
		}

		client.(*rawConn).blockWrite()
		n, err := stream.Read(make([]byte, 8))
		if n != 8 || err != nil {
			client.(*rawConn).unblockWrite()
			t.Fatalf("client: Read: %d, %v", n, err)
		}
		client.Close()
		client.(*rawConn).unblockWrite()

		n, err = stream.Read(make([]byte, 16))
		if n != 0 || err != ECONNCLOSED {
			t.Fatalf("client: Read: %d, %v", n, err)
		}
	})
}

func TestStreamCloseReadError(t *testing.T) {
	startClosingRead := make(chan int, 1)
	startWritingBack := make(chan int, 1)
	runClientServerStream(
		func(client Stream) {
			if 1 != <-startClosingRead {
				return
			}
			client.CloseReadError(EINTERNAL)
			if 1 != <-startWritingBack {
				return
			}
			n, err := client.Write([]byte("Hello, world!"))
			if n != 13 || err != nil {
				t.Fatalf("client: buffered: %d, %v", n, err)
			}
		},
		func(server Stream) {
			defer close(startClosingRead)
			defer close(startWritingBack)
			server.Write([]byte("foobar"))
			startClosingRead <- 1
			n, err := server.WaitAck()
			if n != 6 || err != EINTERNAL {
				t.Fatalf("server: WaitAck: %d, %v", n, err)
			}
			startWritingBack <- 1
			n, err = server.Read(make([]byte, 16))
			if err == nil {
				_, err = server.Peek()
			}
			if n != 13 || err != io.EOF {
				t.Fatalf("server: Read: %d, %v", n, err)
			}
		},
	)
}

func TestStreamCloseReadErrorWithError(t *testing.T) {
	startClosingRead := make(chan int, 1)
	startWritingBack := make(chan int, 1)
	runClientServerStream(
		func(client Stream) {
			if 1 != <-startClosingRead {
				return
			}
			client.CloseReadError(EINTERNAL)
			if 1 != <-startWritingBack {
				return
			}
			n, err := client.Write([]byte("Hello, world!"))
			if n != 13 || err != nil {
				t.Fatalf("client: buffered: %d, %v", n, err)
			}
			client.CloseWithError(ENOROUTE)
		},
		func(server Stream) {
			defer close(startClosingRead)
			defer close(startWritingBack)
			server.Write([]byte("foobar"))
			startClosingRead <- 1
			n, err := server.WaitAck()
			if n != 6 || err != EINTERNAL {
				t.Fatalf("server: WaitAck: %d, %v", n, err)
			}
			startWritingBack <- 1
			n, err = server.Read(make([]byte, 16))
			if err == nil {
				_, err = server.Peek()
			}
			if n != 13 || err != ENOROUTE {
				t.Fatalf("server: Read: %d, %v", n, err)
			}
		},
	)
}

func TestStreamFlush(t *testing.T) {
	clientMayClose := make(chan int, 1)
	serverMayClose := make(chan int, 1)
	runClientServerStream(
		func(client Stream) {
			defer close(serverMayClose)
			n, err := client.Write([]byte("Hello, world!"))
			if n != 13 || err != nil {
				t.Fatalf("client: Write: %d, %v", n, err)
			}
			err = client.Flush()
			if err != nil {
				t.Fatalf("client: Flush: %v", err)
			}
			serverMayClose <- 1
			<-clientMayClose
		},
		func(server Stream) {
			defer close(clientMayClose)
			n, err := server.Write([]byte("foo bar baz"))
			if n != 11 || err != nil {
				t.Fatalf("server: Write: %d, %v", n, err)
			}
			err = server.Flush()
			if err != nil {
				t.Fatalf("server: Flush: %v", err)
			}
			clientMayClose <- 1
			<-serverMayClose
		},
	)
}

func TestStreamWaitAckAny(t *testing.T) {
	serverMayRead := make(chan int, 1)
	clientMayClose := make(chan int, 1)
	clientFinished := make(chan int, 1)
	runClientServerStream(
		func(client Stream) {
			defer close(serverMayRead)
			defer close(clientFinished)
			n, err := client.Write([]byte("Hello, world!"))
			if n != 13 || err != nil {
				t.Fatalf("client: Write: %d, %v", n, err)
			}
			err = client.Flush()
			if err != nil {
				t.Fatalf("client: Flush: %v", err)
			}
			n, err = client.WaitAckAny(0)
			if n != 13 || err != nil {
				t.Fatalf("client: WaitAckAny(0): %d, %v", n, err)
			}
			serverMayRead <- 1
			n, err = client.WaitAckAny(13)
			if n != 13-8 || err != nil {
				t.Fatalf("client: WaitAckAny: %d, %v", n, err)
			}
			clientFinished <- 1
			if 1 != <-clientMayClose {
				return
			}
		},
		func(server Stream) {
			defer close(clientMayClose)
			if 1 != <-serverMayRead {
				return
			}
			n, err := server.Read(make([]byte, 8))
			if n != 8 || err != nil {
				t.Fatalf("server: Read: %d, %v", n, err)
			}
			if 1 != <-clientFinished {
				return
			}
			clientMayClose <- 1
		},
	)
}
