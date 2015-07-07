Checklist for things that can wait, but need to eventually be done:

### Raw connections and streams

- [ ] Add `WaitReadClosed` and `WaitBothClosed` to streams
- [ ] Add `Shutdown` and `WaitDrained` to raw connections
  - Stop accepting new streams and close them with `ESHUTDOWN`
  - Wait until currently accepted streams are closed

### Copper server functionality

- [ ] Optimize subscription updates
  - Endpoints sorted by priority, then distance
  - Random selection proportional to concurrency
- [ ] Queue should `WaitBothClosed` and remove cancelled requests
  - Not sure if should support one-way requests (send a blob, close the stream, don't expect any reply), since quickly become closed (but such requests have no way of knowing if they failed, so probably ok to try them exactly once and give up)
- [ ] Upstream read/write detection in passthru
  - Retries for endpoints when nothing was actually exchanged (except a well-known error code)
- [ ] Fine-grained locking in the server
  - Giant `sync.Mutex` is bad for unrelated streams, should try to replace with `sync.RWMutex` and only lock when need to change things
  - Use locks per-subscription, per-publication, etc.
- [ ] Auto-reconnecting `Client` implementation
  - Automatic re-subscribing on reconnect
  - Automatic re-publishing on reconnect
- [ ] Graceful shutdown for clients (unpublish, wait until drained)
- [ ] Streaming for endpoint updates
- [ ] Authentication and authorization
- [ ] Publishing raw tcp services

### Copper daemon functionality

- [ ] Configuration changes (peers) without restarting
- [ ] Configuration in zookeeper with change auto-detection
