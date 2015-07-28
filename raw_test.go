package copper

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func runRawClientServerStream(clientfunc func(client Stream) error, serverfunc func(stream Stream) error) {
	closeClient := make(chan int, 1)
	rawserver, rawclient := net.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		client := NewRawConn(rawclient, nil, false)
		defer client.Close()
		func() {
			stream, err := client.NewStream()
			if err != nil {
				panic(fmt.Sprintf("client: NewStream: %s", err))
			}
			defer stream.Close()
			err = clientfunc(stream)
			if err != nil {
				stream.CloseWithError(err)
			}
		}()
		<-closeClient
	}()
	go func() {
		defer wg.Done()
		handler := HandlerFunc(func(stream Stream) error {
			defer close(closeClient)
			return serverfunc(stream)
		})
		server := NewRawConn(rawserver, toHandlerFunc(handler), true)
		defer server.Close()
		<-server.Done()
	}()
	wg.Wait()
}

func runRawClientServerHandler(clientfunc func(client RawConn), handler func(stream Stream) error) {
	rawserver, rawclient := net.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		client := NewRawConn(rawclient, nil, false)
		defer client.Close()
		clientfunc(client)
	}()
	go func() {
		defer wg.Done()
		server := NewRawConn(rawserver, toHandlerFunc(handler), true)
		defer server.Close()
		<-server.Done()
	}()
	wg.Wait()
}

func TestRawConnStreams(t *testing.T) {
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
		var counter int64
		handler := HandlerFunc(func(stream Stream) error {
			targetID := atomic.AddInt64(&counter, 1)
			r := bufio.NewReader(stream)
			line, err := r.ReadString('\n')
			if err != io.EOF {
				t.Fatalf("handler: ReadString: expected io.EOF, got %#v", err)
			}
			if stream.(*rawStream).flags&flagStreamSeenEOF == 0 {
				t.Fatalf("handler: stream did not see EOF yet")
			}
			_, err = fmt.Fprintf(stream, "%d: '%s'", targetID, line)
			if err != nil {
				t.Fatalf("handler: Fprintf: unexpected error: %v", err)
			}
			// Common sense dictates, that data from Fprintf should reach
			// the other side when we close the stream!
			return closeErrors[targetID]
		})
		server := NewRawConn(serverconn, handler, true)
		defer server.Close()

		stream, err := server.NewStream()
		if err != nil {
			t.Fatalf("server: NewStream: unexpected error: %v", err)
		}
		n, err := stream.Read(make([]byte, 256))
		if n != 0 || err != ENOTARGET {
			t.Fatalf("server: Read: expected ENOTARGET, got: %d, %v", n, err)
		}
		serverReady <- 1

		<-server.Done()
		err = server.Err()
		if err != ECONNSHUTDOWN {
			t.Fatalf("server: expected ECONNSHUTDOWN, got: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		client := NewRawConn(clientconn, nil, false)
		defer client.Close()
		if 1 != <-serverReady {
			return
		}

		messages := []string{
			"hello",
			"world stuff",
			"some unexpected message",
		}
		expectedError := []error{
			io.EOF,
			ENOROUTE,
			ENOTARGET,
		}
		expectedResponse := []string{
			"1: 'hello'",
			"2: 'world stuff'",
			"3: 'some unexpected message'",
		}
		for index, message := range messages {
			func() {
				client.(*rawConn).blockWrite()
				stream, err := client.NewStream()
				if err != nil {
					t.Fatalf("client: NewStream(): unexpected error: %v", err)
				}
				defer stream.Close()
				_, err = stream.Write([]byte(message))
				if err != nil {
					t.Fatalf("client: Write(%d): unexpected error: %v", index+1, err)
				}
				err = stream.CloseWrite()
				if err != nil {
					t.Fatalf("client: CloseWrite(%d): unexpected error: %v", index+1, err)
				}
				client.(*rawConn).unblockWrite()

				r := bufio.NewReader(stream)
				line, err := r.ReadString('\n')
				if line != expectedResponse[index] || err != expectedError[index] {
					t.Fatalf("client: ReadString(%d): expected %q, %v, got: %q, %v",
						index+1,
						expectedResponse[index], expectedError[index],
						line, err)
				}
			}()
		}
	}()
	wg.Wait()
}

// TODO: remove and switch to new functions
func runRawClientServer(handler Handler, clientfunc func(client RawConn)) {
	rawserver, rawclient := net.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		server := NewRawConn(rawserver, handler, true)
		defer server.Close()
		<-server.Done()
	}()
	go func() {
		defer wg.Done()
		client := NewRawConn(rawclient, nil, false)
		defer client.Close()
		clientfunc(client)
	}()
	wg.Wait()
}

func TestRawConnPing(t *testing.T) {
	runRawClientServer(nil, func(client RawConn) {
		var ch1, ch2 <-chan error
		// simple ping
		if err := <-client.Ping(PingDataInt64(123)); err != nil {
			t.Fatalf("client: Ping: unexpected error: %v", err)
		}
		// two simultaneous pings, same id
		ch1 = client.Ping(PingDataInt64(42))
		ch2 = client.Ping(PingDataInt64(42))
		if err := <-ch1; err != nil {
			t.Fatalf("client: Ping: unexpected error: %v", err)
		}
		if err := <-ch2; err != nil {
			t.Fatalf("client: Ping: unexpected error: %v", err)
		}
		// two simultaneous pings that fail before getting response
		client.(*rawConn).blockWrite()
		ch1 = client.Ping(PingDataInt64(51))
		ch2 = client.Ping(PingDataInt64(51))
		client.Close()
		client.(*rawConn).unblockWrite()
		if err := <-ch1; err != ECONNCLOSED {
			t.Fatalf("client: Ping: expected ECONNCLOSED, not %v", err)
		}
		if err := <-ch2; err != ECONNCLOSED {
			t.Fatalf("client: Ping: expected ECONNCLOSED, not %v", err)
		}
		// simple ping on closed connection should immediately fail
		if err := <-client.Ping(PingDataInt64(321)); err != ECONNCLOSED {
			t.Fatalf("client: Ping: expected ECONNCLOSED, not %v", err)
		}
	})
}

func TestRawStreamBigWrite(t *testing.T) {
	runRawClientServer(HandlerFunc(func(stream Stream) error {
		stream.Peek()
		return ENOROUTE
	}), func(client RawConn) {
		stream, err := client.NewStream()
		if err != nil {
			t.Fatalf("client: NewStream: %s", err)
		}
		defer stream.Close()
		n, err := stream.Write(make([]byte, 65536+65536+16))
		if (n != 65536 && n != 65536*2) || err != ENOROUTE {
			t.Fatalf("client: Write: unexpected result: %d, %v", n, err)
		}
	})
}

func TestRawStreamReadDeadline(t *testing.T) {
	runRawClientServer(HandlerFunc(func(stream Stream) error {
		stream.Read(make([]byte, 256))
		return nil
	}), func(client RawConn) {
		var deadline time.Time
		stream, err := client.NewStream()
		if err != nil {
			t.Fatalf("client: NewStream: %s", err)
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

func TestRawStreamWriteDeadline(t *testing.T) {
	runRawClientServer(HandlerFunc(func(stream Stream) error {
		stream.Write(make([]byte, 65536+65536+16))
		return nil
	}), func(client RawConn) {
		var deadline time.Time
		stream, err := client.NewStream()
		if err != nil {
			t.Fatalf("client: NewStream: %s", err)
		}
		defer stream.Close()

		deadline = time.Now().Add(5 * time.Millisecond)
		stream.SetWriteDeadline(deadline)
		n, err := stream.Write(make([]byte, 65536+65536+16))
		if n != 65536*2 || !isTimeout(err) {
			t.Fatalf("client: Write: expected timeout, got: %d, %v", n, err)
		}
		if deadline.After(time.Now()) {
			t.Fatalf("client: Write: expected to timeout after the deadline, not before")
		}
	})
}

func TestRawStreamCloseRead(t *testing.T) {
	serverMayWrite := make(chan int, 1)
	serverMayClose := make(chan int, 1)
	runRawClientServer(HandlerFunc(func(stream Stream) error {
		_, err := stream.Peek()
		if err != nil {
			t.Fatalf("server: Peek: %v", err)
		}
		err = stream.CloseRead()
		if err != nil {
			t.Fatalf("server: CloseRead: %v", err)
		}
		if 1 != <-serverMayWrite {
			return nil
		}
		n, err := stream.Write([]byte("hello"))
		if n != 5 || err != nil {
			t.Fatalf("server: Write: %d, %v", n, err)
		}
		<-serverMayClose
		return nil
	}), func(client RawConn) {
		defer close(serverMayClose)
		defer close(serverMayWrite)

		stream, err := client.NewStream()
		if err != nil {
			t.Fatalf("client: NewStream: %v", err)
		}
		defer stream.Close()

		stream.Write([]byte("foobar"))
		<-stream.WriteClosed()
		if stream.WriteErr() != ECLOSED {
			t.Fatalf("client: WriteErr: %v", stream.WriteErr())
		}
		serverMayWrite <- 1
		n, err := stream.Read(make([]byte, 16))
		if n != 5 || err != nil {
			t.Fatalf("client: Read: %d, %v", n, err)
		}
		serverMayClose <- 1
	})
}

func TestRawStreamCloseWithData(t *testing.T) {
	var goahead1 sync.Mutex
	var goahead2 sync.Mutex
	goahead1.Lock()
	goahead2.Lock()
	runRawClientServer(HandlerFunc(func(stream Stream) error {
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
		return nil
	}), func(client RawConn) {
		stream, err := client.NewStream()
		if err != nil {
			t.Fatalf("server: NewStream: %v", err)
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

func TestRawStreamCloseAfterData(t *testing.T) {
	var goahead1 sync.Mutex
	var goahead2 sync.Mutex
	var goahead3 sync.Mutex
	goahead1.Lock()
	goahead2.Lock()
	goahead3.Lock()
	runRawClientServer(HandlerFunc(func(stream Stream) error {
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
		return nil
	}), func(client RawConn) {
		stream, err := client.NewStream()
		if err != nil {
			t.Fatalf("server: NewStream: %v", err)
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

func TestRawStreamConnClose(t *testing.T) {
	runRawClientServer(HandlerFunc(func(stream Stream) error {
		n, err := stream.Write([]byte("Hello, world!"))
		if n != 13 || err != nil {
			t.Fatalf("server: Write: %d, %v", n, err)
		}
		<-stream.WriteClosed()
		if stream.WriteErr() != ECONNSHUTDOWN {
			t.Fatalf("server: WriteErr: %v", stream.WriteErr())
		}
		return nil
	}), func(client RawConn) {
		stream, err := client.NewStream()
		if err != nil {
			t.Fatalf("client: NewStream: %v", err)
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
		if n != 5 || err != ECONNCLOSED {
			t.Fatalf("client: Read: %d, %v", n, err)
		}
	})
}

func TestRawStreamCloseReadError(t *testing.T) {
	startClosingRead := make(chan int, 1)
	startWritingBack := make(chan int, 1)
	runRawClientServerStream(
		func(client Stream) error {
			if 1 != <-startClosingRead {
				return nil
			}
			client.CloseReadError(EINTERNAL)
			if 1 != <-startWritingBack {
				return nil
			}
			n, err := client.Write([]byte("Hello, world!"))
			if n != 13 || err != nil {
				t.Fatalf("client: buffered: %d, %v", n, err)
			}
			return nil
		},
		func(server Stream) error {
			defer close(startClosingRead)
			defer close(startWritingBack)
			server.Write([]byte("foobar"))
			startClosingRead <- 1
			<-server.WriteClosed()
			if server.WriteErr() != EINTERNAL {
				t.Fatalf("server: WriteErr: %v", server.WriteErr())
			}
			startWritingBack <- 1
			n, err := server.Read(make([]byte, 16))
			if err == nil {
				_, err = server.Peek()
			}
			if n != 13 || err != io.EOF {
				t.Fatalf("server: Read: %d, %v", n, err)
			}
			return nil
		},
	)
}

func TestRawStreamCloseReadErrorWithError(t *testing.T) {
	startClosingRead := make(chan int, 1)
	startWritingBack := make(chan int, 1)
	runRawClientServerStream(
		func(client Stream) error {
			if 1 != <-startClosingRead {
				return nil
			}
			client.CloseReadError(EINTERNAL)
			if 1 != <-startWritingBack {
				return nil
			}
			n, err := client.Write([]byte("Hello, world!"))
			if n != 13 || err != nil {
				t.Fatalf("client: buffered: %d, %v", n, err)
			}
			return ENOROUTE
		},
		func(server Stream) error {
			defer close(startClosingRead)
			defer close(startWritingBack)
			server.Write([]byte("foobar"))
			startClosingRead <- 1
			<-server.WriteClosed()
			if server.WriteErr() != EINTERNAL {
				t.Fatalf("server: WriteErr: %v", server.WriteErr())
			}
			startWritingBack <- 1
			n, err := server.Read(make([]byte, 16))
			if err == nil {
				_, err = server.Peek()
			}
			if n != 13 || err != ENOROUTE {
				t.Fatalf("server: Read: %d, %v", n, err)
			}
			return nil
		},
	)
}

func TestRawStreamFlush(t *testing.T) {
	clientMayClose := make(chan int, 1)
	serverMayClose := make(chan int, 1)
	runRawClientServerStream(
		func(client Stream) error {
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
			return nil
		},
		func(server Stream) error {
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
			return nil
		},
	)
}

func TestRawStreamAcknowledge(t *testing.T) {
	runRawClientServerStream(
		func(client Stream) error {
			<-client.Acknowledged()
			if client.IsAcknowledged() {
				t.Fatal("client: remote unexpectedly acknowledged")
			}
			return nil
		},
		func(server Stream) error {
			return nil
		},
	)
	runRawClientServerStream(
		func(client Stream) error {
			<-client.Acknowledged()
			if !client.IsAcknowledged() {
				t.Fatal("client: remote did not acknowledge")
			}
			return nil
		},
		func(server Stream) error {
			server.Acknowledge()
			return nil
		},
	)
}
