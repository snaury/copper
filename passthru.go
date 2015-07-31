package copper

import (
	"sync"
)

func passthru(dst, src Stream, waitack bool) {
	go func() {
		<-src.Acknowledged()
		if src.IsAcknowledged() {
			dst.Acknowledge()
		}
	}()
	var write sync.Mutex
	go func() {
		<-dst.WriteClosed()
		write.Lock()
		defer write.Unlock()
		src.CloseReadWithError(dst.WriteErr())
	}()
	for {
		buf, err := src.Peek()
		if len(buf) > 0 {
			write.Lock()
			n, werr := dst.Write(buf)
			if n > 0 {
				if waitack {
					<-dst.Acknowledged()
					if werr == nil {
						// Recheck if write side was closed
						werr = dst.WriteErr()
					}
					if dst.IsAcknowledged() || werr == nil {
						// Discard input if acknowledged, or write side is not
						// yet closed. Remember, that acknowledgements are sent
						// on the write side, but received on the read side, so
						// the above tells us that if CloseWrite was called by
						// the remote, then CloseRead was not, which may
						// indicate it is reading our data. Otherwise the write
						// side is done, we assume the request was lost and src
						// may be reused for another attempt.
						// TODO: implement src reuse
						src.Discard(n)
					}
					waitack = false
				} else {
					src.Discard(n)
				}
			}
			write.Unlock()
			if werr != nil {
				src.CloseReadWithError(werr)
				return
			}
		}
		if err != nil {
			if src.IsAcknowledged() {
				dst.Acknowledge()
			}
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
