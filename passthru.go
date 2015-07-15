package copper

import (
	"io"
	"sync"
	"sync/atomic"
)

func passthru(dst, src Stream) {
	var write sync.Mutex
	var writeclosed uint32
	go func() {
		<-dst.WriteClosed()
		write.Lock()
		defer write.Unlock()
		atomic.AddUint32(&writeclosed, 1)
		err := dst.WriteErr()
		if err == ECLOSED {
			src.CloseRead()
		} else {
			src.CloseReadError(err)
		}
	}()
	for {
		buf, err := src.Peek()
		if atomic.LoadUint32(&writeclosed) > 0 {
			// Don't react to CloseRead in the above goroutine
			return
		}
		if len(buf) > 0 {
			write.Lock()
			n, werr := dst.Write(buf)
			if n > 0 {
				src.Discard(n)
			}
			write.Unlock()
			if werr != nil {
				if werr == ECLOSED {
					src.CloseRead()
				} else {
					src.CloseReadError(werr)
				}
				return
			}
		}
		if err != nil {
			if err == io.EOF {
				dst.CloseWrite()
			} else {
				dst.CloseWithError(err)
			}
			return
		}
	}
}

func passthruBoth(local, remote Stream) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		passthru(remote, local)
	}()
	passthru(local, remote)
	wg.Wait()
}
