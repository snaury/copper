package copperd

import (
	"io"
	"sync"
	"sync/atomic"

	"github.com/snaury/copper"
)

func passthru(dst, src copper.Stream) {
	var writeclosed uint32
	go func() {
		err := dst.WaitWriteClosed()
		atomic.AddUint32(&writeclosed, 1)
		if err == copper.ESTREAMCLOSED {
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
			n, werr := dst.Write(buf)
			if n > 0 {
				src.Discard(n)
			}
			if werr != nil {
				if werr == copper.ESTREAMCLOSED {
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

func passthruBoth(local, remote copper.Stream) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		passthru(remote, local)
	}()
	go func() {
		defer wg.Done()
		passthru(local, remote)
	}()
	wg.Wait()
}
