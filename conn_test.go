package copper

import (
	"bufio"
	"fmt"
	"io"
	//"log"
	"net"
	"sync"
	"testing"
)

func TestConn(t *testing.T) {
	var wg sync.WaitGroup
	serverconn, clientconn := net.Pipe()
	wg.Add(2)
	go func() {
		defer wg.Done()
		handler := func(target int64, stream Stream) {
			defer stream.Close()
			r := bufio.NewReader(stream)
			line, err := r.ReadString('\n')
			if err != io.EOF {
				t.Fatalf("handler: ReadString: expected io.EOF, got %#v", err)
			}
			_, err = fmt.Fprintf(stream, "%d: '%s'", target, line)
			if err != nil {
				t.Fatalf("handler: Fprintf: unexpected error: %v", err)
			}
			// Common sense dictates, that data from Fprintf should reach
			// the other side when we defer stream.Close()!
		}
		server := NewConn(serverconn, StreamHandlerFunc(handler), true)
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

		stream, err := client.OpenStream(42)
		if err != nil {
			t.Fatalf("client: OpenStream: unexpected error: %v", err)
		}
		defer stream.Close()
		_, err = stream.Write([]byte("hello"))
		if err != nil {
			t.Fatalf("client: Write: unexpected error: %v", err)
		}
		err = stream.CloseWrite()
		if err != nil {
			t.Fatalf("client: CloseWrite: unexpected error: %v", err)
		}
		r := bufio.NewReader(stream)
		line, err := r.ReadString('\n')
		if err != io.EOF {
			t.Fatalf("client: ReadString: expected io.EOF, got: %v", err)
		}
		if line != "42: 'hello'" {
			t.Fatalf("client: ReadString: unexpected response: %q", line)
		}
	}()
	wg.Wait()
}
