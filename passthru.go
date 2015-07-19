package copper

import (
	"io"
	"sync"
)

func passthru(dst, src Stream, waitack bool) {
	var write sync.Mutex
	go func() {
		<-dst.WriteClosed()
		write.Lock()
		defer write.Unlock()
		err := dst.WriteErr()
		if err == ECLOSED {
			src.CloseRead()
		} else {
			src.CloseReadError(err)
		}
	}()
	for {
		buf, err := src.Peek()
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
			if waitack {
				<-dst.Acknowledged()
				if dst.IsAcknowledged() {
					src.Acknowledge()
				}
				waitack = false
			}
		}
		if err != nil {
			if dst.WriteErr() == nil {
				if err == io.EOF {
					dst.CloseWrite()
				} else {
					dst.CloseWithError(err)
				}
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
		passthru(remote, local, true)
	}()
	passthru(local, remote, false)
	wg.Wait()
}
