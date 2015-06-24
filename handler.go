package copper

import (
	"sync"
)

// StreamHandler is used to handle incoming streams
type StreamHandler interface {
	HandleStream(target int64, stream Stream)
}

// StreamHandlerFunc wraps a function to conform with StreamHandler interface
type StreamHandlerFunc func(target int64, stream Stream)

var _ StreamHandler = StreamHandlerFunc(nil)

// HandleStream calls the underlying function
func (f StreamHandlerFunc) HandleStream(target int64, stream Stream) {
	f(target, stream)
}

// StreamHandlerMap allows dynamic dispatching and allocation of targets
type StreamHandlerMap struct {
	lock       sync.RWMutex
	targets    map[int64]StreamHandler
	nexttarget int64
}

var _ StreamHandler = &StreamHandlerMap{}

// NewStreamHandlerMap creates a new stream handler map
func NewStreamHandlerMap(mainhandler StreamHandler) *StreamHandlerMap {
	return &StreamHandlerMap{
		targets: map[int64]StreamHandler{
			0: mainhandler,
		},
		nexttarget: 1,
	}
}

func (h *StreamHandlerMap) find(target int64) StreamHandler {
	h.lock.RLock()
	defer h.lock.RUnlock()
	return h.targets[target]
}

// HandleStream dispatches to a registered handler
func (h *StreamHandlerMap) HandleStream(target int64, stream Stream) {
	handler := h.find(target)
	if handler != nil {
		handler.HandleStream(target, stream)
	} else {
		stream.CloseWithError(ENOTARGET)
	}
}

// Add registers a handler and returns its target id
func (h *StreamHandlerMap) Add(handler StreamHandler) int64 {
	h.lock.Lock()
	defer h.lock.Unlock()
	target := h.nexttarget
	h.nexttarget++
	h.targets[target] = handler
	return target
}

// Remove removes a previously registered handler by its target id
func (h *StreamHandlerMap) Remove(target int64) {
	h.lock.Lock()
	defer h.lock.Unlock()
	delete(h.targets, target)
}
