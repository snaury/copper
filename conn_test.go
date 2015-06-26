package copper

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
)

func TestConn(t *testing.T) {
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
		handler := StreamHandlerFunc(func(target int64, stream Stream) {
			defer func() {
				stream.CloseWithError(closeErrors[target])
			}()
			r := bufio.NewReader(stream)
			line, err := r.ReadString('\n')
			if err != io.EOF {
				t.Fatalf("handler: ReadString: expected io.EOF, got %#v", err)
			}
			if stream.(*rawStream).flags&flagStreamSeenEOF == 0 {
				t.Fatalf("handler: stream did not see EOF yet")
			}
			_, err = fmt.Fprintf(stream, "%d: '%s'", target, line)
			if err != nil {
				t.Fatalf("handler: Fprintf: unexpected error: %v", err)
			}
			// Common sense dictates, that data from Fprintf should reach
			// the other side when we close the stream!
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
