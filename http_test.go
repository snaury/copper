package copper

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"testing"
)

func addHTTPListener(server Server, address string) string {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Failed to listen: %s", err)
	}
	err = server.AddHTTPListener(listener)
	if err != nil {
		log.Fatalf("Failed to add listener: %s", err)
	}
	return listener.Addr().String()
}

func readResponseBody(res *http.Response) string {
	var buf [65536]byte
	n, err := io.ReadFull(res.Body, buf[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		log.Fatalf("failed to read response: %s", err)
	}
	return string(buf[:n])
}

func TestHTTP(t *testing.T) {
	server, addr := runServer("localhost:0")
	defer server.Close()
	httpaddr := addHTTPListener(server, "localhost:0")

	makeRequest := func(path string) (int, string) {
		t.Logf("makeRequest(%q)", path)
		res, err := http.Get("http://" + httpaddr + path)
		if err != nil {
			t.Fatalf("Get(%q): %s", path, err)
		}
		defer res.Body.Close()
		body := readResponseBody(res)
		return res.StatusCode, body
	}

	makeServiceRequest := func(service, path string) (int, string) {
		t.Logf("makeServiceRequest(%q, %q)", service, path)
		req, err := http.NewRequest("GET", "http://"+httpaddr+path, nil)
		if err != nil {
			t.Fatalf("NewRequest(%q, %q): %s", service, path, err)
		}
		req.Header.Add("X-Copper-Service", service)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Get(%q, %q): %s", service, path, err)
		}
		defer res.Body.Close()
		body := readResponseBody(res)
		return res.StatusCode, body
	}

	client := connectClient(addr)
	defer client.Close()

	pub1, err := client.Publish(
		"http:/service/",
		PublishSettings{
			Concurrency: 1,
			QueueSize:   64,
		},
		HTTPHandler(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			rw.WriteHeader(200)
			fmt.Fprintf(rw, "pub1: %s", req.URL.Path)
		})),
	)
	if err != nil {
		t.Fatalf("Publish(1): %s", err)
	}
	defer pub1.Stop()

	pub2, err := client.Publish(
		"http:/service/special",
		PublishSettings{
			Concurrency: 1,
			QueueSize:   64,
		},
		HTTPHandler(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			rw.WriteHeader(200)
			fmt.Fprintf(rw, "pub2: %s", req.URL.Path)
		})),
	)
	if err != nil {
		t.Fatalf("Publish(2): %s", err)
	}
	defer pub2.Stop()

	status, body := makeRequest("/service/special/whatever")
	if status != 200 || body != "pub1: /service/special/whatever" {
		t.Fatalf("Case 1: %d: %s", status, body)
	}

	status, body = makeRequest("/service/special")
	if status != 200 || body != "pub2: /service/special" {
		t.Fatalf("Case 2: %d: %s", status, body)
	}

	status, body = makeRequest("/foo/bar/baz")
	if status != 404 {
		t.Fatalf("Case 3: %d: %s", status, body)
	}

	status, body = makeServiceRequest("/service/", "/foo/bar/baz")
	if status != 200 || body != "pub1: /foo/bar/baz" {
		t.Fatalf("Case 4: %d: %s", status, body)
	}

	status, body = makeServiceRequest("foobar", "/service/special/whatever")
	if status != 404 {
		t.Fatalf("Case 5: %d: %s", status, body)
	}
}
