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

		stream, err := server.OpenStream(51)
		if err != nil {
			t.Fatalf("server: OpenStream: unexpected error: %v", err)
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
				stream, err := client.OpenStream(target)
				if err != nil {
					t.Fatalf("client: OpenStream(%d): unexpected error: %v", target, err)
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

func TestStreamBigWrite(t *testing.T) {
	runClientServer(StreamHandlerFunc(func(stream Stream) {
		time.Sleep(50 * time.Millisecond)
		stream.CloseWithError(ENOROUTE)
	}), func(client Conn) {
		stream, err := client.OpenStream(0)
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
			stream, err := client.OpenStream(target)
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
				if err != ECLOSED {
					t.Fatalf("client: Flush(%d): %v (expected ECLOSED)", target, err)
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
