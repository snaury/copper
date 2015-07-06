package copper

import (
	"sync"
)

// Handler is used to handle incoming streams
type Handler interface {
	Handle(stream Stream)
}

// HandlerFunc wraps a function to conform with Handler interface
type HandlerFunc func(stream Stream)

var _ Handler = HandlerFunc(nil)

// Handle calls the underlying function
func (f HandlerFunc) Handle(stream Stream) {
	f(stream)
}

// HandlerMap allows dynamic dispatching and allocation of targets
type HandlerMap struct {
	lock       sync.RWMutex
	targets    map[int64]Handler
	nexttarget int64
}

var _ Handler = &HandlerMap{}

// NewHandlerMap creates a new stream handler map
func NewHandlerMap(mainhandler Handler) *HandlerMap {
	return &HandlerMap{
		targets: map[int64]Handler{
			0: mainhandler,
		},
		nexttarget: 1,
	}
}

func (h *HandlerMap) find(target int64) Handler {
	h.lock.RLock()
	defer h.lock.RUnlock()
	return h.targets[target]
}

// Handle dispatches to a registered handler
func (h *HandlerMap) Handle(stream Stream) {
	target := stream.TargetID()
	handler := h.find(target)
	if handler != nil {
		handler.Handle(stream)
	} else {
		stream.CloseWithError(ENOTARGET)
	}
}

// Add registers a handler and returns its target id
func (h *HandlerMap) Add(handler Handler) int64 {
	h.lock.Lock()
	defer h.lock.Unlock()
	target := h.nexttarget
	h.nexttarget++
	h.targets[target] = handler
	return target
}

// Remove removes a previously registered handler by its target id
func (h *HandlerMap) Remove(target int64) {
	h.lock.Lock()
	defer h.lock.Unlock()
	delete(h.targets, target)
}
