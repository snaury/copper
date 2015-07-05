package benchmark

import (
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/benchmark/stats"
)

func runCopperCalls(b *testing.B, maxConcurrentCalls int) {
	s := stats.AddStats(b, 38)
	b.StopTimer()
	addr, stopper := startCopper("localhost:0")
	defer stopper()

	pubStopper := publishCopperService(addr, maxConcurrentCalls)
	defer pubStopper()

	sub, subStopper := subscribeCopperService(addr)
	defer subStopper()

	for i := 0; i < 10; i++ {
		callCopperService(sub)
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
				callCopperService(sub)
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

func BenchmarkCopperCall1(b *testing.B) {
	runCopperCalls(b, 1)
}

func BenchmarkCopperCall8(b *testing.B) {
	runCopperCalls(b, 8)
}

func BenchmarkCopperCall64(b *testing.B) {
	runCopperCalls(b, 64)
}

func BenchmarkCopperCall512(b *testing.B) {
	runCopperCalls(b, 512)
}
