package copper

import (
	"sync"
)

// Handler is used to handle incoming streams
type Handler interface {
	ServeCopper(stream Stream) error
}

// HandlerFunc wraps a function to conform with Handler interface
type HandlerFunc func(stream Stream) error

var _ Handler = HandlerFunc(nil)

// ServeCopper calls the underlying function
func (f HandlerFunc) ServeCopper(stream Stream) error {
	return f(stream)
}

// HandlerMap allows dynamic dispatching and allocation of targets
type HandlerMap struct {
	lock       sync.RWMutex
	targets    map[int64]Handler
	nexttarget int64
}

// NewHandlerMap creates a new stream handler map
func NewHandlerMap(mainhandler Handler) *HandlerMap {
	return &HandlerMap{
		targets: map[int64]Handler{
			0: mainhandler,
		},
		nexttarget: 1,
	}
}

// Find returns a previously registered handler for a targetID
func (h *HandlerMap) Find(target int64) Handler {
	h.lock.RLock()
	defer h.lock.RUnlock()
	return h.targets[target]
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
