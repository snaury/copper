package benchmark

import (
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/benchmark/stats"
)

func runRawCalls(b *testing.B, maxConcurrentCalls int) {
	s := stats.AddStats(b, 38)
	b.StopTimer()
	target, stopper := startRawServer("localhost:0")
	defer stopper()

	conn := dialRawServer(target)
	defer conn.Close()
	for i := 0; i < 10; i++ {
		callRawServer(conn)
	}

	var lock sync.Mutex
	var wg sync.WaitGroup

	ch := make(chan int, maxConcurrentCalls*4)
	for i := 0; i < maxConcurrentCalls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _ = range ch {
				start := time.Now()
				callRawServer(conn)
				elapsed := time.Since(start)

				lock.Lock()
				s.Add(elapsed)
				lock.Unlock()
			}
		}()
	}

	b.StartTimer()
	for i := 0; i < b.N; i++ {
		ch <- i
	}
	close(ch)
	wg.Wait()
	b.StopTimer()
}

func BenchmarkRawCall1(b *testing.B) {
	runRawCalls(b, 1)
}

func BenchmarkRawCall8(b *testing.B) {
	runRawCalls(b, 4)
}

func BenchmarkRawCall64(b *testing.B) {
	runRawCalls(b, 64)
}

func BenchmarkRawCall512(b *testing.B) {
	runRawCalls(b, 64)
}

func BenchmarkRawRead(b *testing.B) {
	b.StopTimer()
	totalBytes := 65536 * int64(b.N)
	b.SetBytes(65536)
	target, stopper := startRawServer("localhost:0")
	defer stopper()
	conn := dialRawServer(target)
	defer conn.Close()
	b.StartTimer()
	benchreadRawServer(conn, totalBytes)
	b.StopTimer()
}
