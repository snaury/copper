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

func TestConnStreams(t *testing.T) {
	var wg sync.WaitGroup
	serverconn, clientconn := net.Pipe()
	wg.Add(2)
	go func() {
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

		err = server.Wait()
		if err != ECONNCLOSED {
			t.Fatalf("server: Wait: expected ECONNCLOSED, got: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		client := NewConn(clientconn, nil, false)
		defer client.Close()

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
		ch1 = client.Ping(51)
		ch2 = client.Ping(51)
		client.Close()
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
	runClientServer(nil, func(client Conn) {
		stream, err := client.Open(42)
		if err != nil {
			t.Fatalf("client: Open: %s", err)
		}
		defer stream.Close()
		if stream.StreamID() != 1 {
			t.Fatalf("client: unexpected stream id: %d (expected 1)", stream.StreamID())
		}
		_, err = stream.Write([]byte("foobar"))
		if err != nil {
			t.Fatalf("client: Write: %s", err)
		}
		stream.CloseWrite()
		_, err = stream.Read(make([]byte, 256))
		if err != ENOTARGET {
			t.Fatalf("client: Read: %s (expected ENOTARGET)", err)
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
		time.Sleep(50 * time.Millisecond)
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

		deadline = time.Now().Add(50 * time.Millisecond)
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

		deadline = time.Now().Add(50 * time.Millisecond)
		stream.SetWriteDeadline(deadline)
		n, err := stream.Write(make([]byte, 65536+16))
		if n != 65536 || !isTimeout(err) {
			t.Fatalf("client: Write: expected timeout, got: %d, %v", n, err)
		}
		if deadline.After(time.Now()) {
			t.Fatalf("client: Write: expected to timeout after the deadline, not before")
		}

		deadline = time.Now().Add(50 * time.Millisecond)
		stream.SetWriteDeadline(deadline)
		err = stream.Flush()
		if !isTimeout(err) {
			t.Fatalf("client: Flush: expected timeout, got: %v", err)
		}
		if deadline.After(time.Now()) {
			t.Fatalf("client: Flush: expected to timeout after the deadline, not before")
		}
	})
}

func TestStreamFlush(t *testing.T) {
	var sleepfinished uint32
	runClientServer(StreamHandlerFunc(func(stream Stream) {
		switch stream.TargetID() {
		case 1:
			// 1st case: close the connection
			time.Sleep(50 * time.Millisecond)
			atomic.StoreUint32(&sleepfinished, 1)
			err := stream.Close()
			if err != nil {
				t.Fatalf("server: Close: %s", err)
			}
		case 2:
			// 2nd case: close with ENOROUTE
			time.Sleep(50 * time.Millisecond)
			atomic.StoreUint32(&sleepfinished, 1)
			err := stream.CloseWithError(ENOROUTE)
			if err != nil {
				t.Fatalf("server: CloseWithError: %s", err)
			}
		case 3:
			// 3rd case: read data and sleep a little
			time.Sleep(50 * time.Millisecond)
			atomic.StoreUint32(&sleepfinished, 1)
			_, err := stream.Read(make([]byte, 16))
			if err != nil {
				t.Fatalf("server: Read: %s", err)
			}
			time.Sleep(50 * time.Millisecond)
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
			stream, err := client.Open(target)
			if err != nil {
				t.Fatalf("client: Open: %s", err)
			}
			defer stream.Close()
			_, err = stream.Write([]byte("Hello, world!"))
			if err != nil {
				t.Fatalf("client: Write: %s", err)
			}
			err = stream.Flush()
			switch target {
			case 1:
				if err != ESTREAMCLOSED {
					t.Fatalf("client: Flush(%d): %v (expected ESTREAMCLOSED)", target, err)
				}
			case 2:
				if err != ENOROUTE {
					t.Fatalf("client: Flush(%d): %v (expected ETARGET)", target, err)
				}
			default:
				if err != nil {
					t.Fatalf("client: Flush(%d): %v (expected <nil>)", target, err)
				}
			}
			if atomic.LoadUint32(&sleepfinished) != 1 {
				t.Fatalf("client: Flush(%d): sleep did not finish before flush returned", target)
			}
			atomic.StoreUint32(&sleepfinished, 0)
		}
	})
}
