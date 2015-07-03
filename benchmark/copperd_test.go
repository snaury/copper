package benchmark

import (
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/benchmark/stats"
)

func runCopperdCalls(b *testing.B, maxConcurrentCalls int) {
	s := stats.AddStats(b, 38)
	b.StopTimer()
	addr, stopper := startCopperd("localhost:0")
	defer stopper()

	pubStopper := publishCopperdService(addr, maxConcurrentCalls)
	defer pubStopper()

	sub, subStopper := subscribeCopperdService(addr)
	defer subStopper()

	for i := 0; i < 10; i++ {
		callCopperdService(sub)
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
				callCopperdService(sub)
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
}

func BenchmarkCopperdCall1(b *testing.B) {
	runCopperdCalls(b, 1)
}

func BenchmarkCopperdCall8(b *testing.B) {
	runCopperdCalls(b, 8)
}

func BenchmarkCopperdCall64(b *testing.B) {
	runCopperdCalls(b, 64)
}

func BenchmarkCopperdCall512(b *testing.B) {
	runCopperdCalls(b, 512)
}
