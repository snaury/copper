package benchmark

import (
	"os"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/benchmark/stats"
)

func runCalls(b *testing.B, maxConcurrentCalls int) {
	s := stats.AddStats(b, 38)
	b.StopTimer()
	target, stopper := startServer("localhost:0")
	defer stopper()

	conn := dial(target)
	defer conn.Close()
	for i := 0; i < 10; i++ {
		call(conn)
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
				call(conn)
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

func BenchmarkCall1(b *testing.B) {
	runCalls(b, 1)
}

func BenchmarkCall8(b *testing.B) {
	runCalls(b, 4)
}

func BenchmarkCall64(b *testing.B) {
	runCalls(b, 64)
}

func BenchmarkCall512(b *testing.B) {
	runCalls(b, 64)
}

func TestMain(m *testing.M) {
	os.Exit(stats.RunTestMain(m))
}
