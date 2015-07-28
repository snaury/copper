package copper

import (
	"sync"
)

func passthru(dst, src Stream, waitack bool) {
	var write sync.Mutex
	go func() {
		<-dst.WriteClosed()
		write.Lock()
		defer write.Unlock()
		err := dst.WriteErr()
		src.CloseReadWithError(err)
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
				src.CloseReadWithError(werr)
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
			dst.CloseWriteWithError(err)
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
