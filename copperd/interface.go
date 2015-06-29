package copperd

import (
	"github.com/snaury/copper"
	"github.com/snaury/copper/copperd/protocol"
	"net"
)

// EndpointsEventType describes type of the event
type EndpointsEventType protocol.Endpoint_EventType

const (
	// EndpointsAdded is returned when endpoints have been added
	EndpointsAdded = protocol.Endpoint_ADDED

	// EndpointsRemoved is returned when endpoints have been removed
	EndpointsRemoved = protocol.Endpoint_REMOVED

	// EndpointsReplaced is returned when endpoints have been replaced
	EndpointsReplaced = protocol.Endpoint_REPLACED
)

// Route describes the target service for a route
type Route struct {
	Service  string
	Weight   uint32
	Distance uint32
}

// Endpoint describes access endpoints for services
type Endpoint struct {
	Address string
	Network string
}

// SubscribeOption describes the service, how far copperd is allowed to reach
// it and how many retries are allowed when attempts to reach it fail.
type SubscribeOption struct {
	Service    string
	Distance   uint32
	MaxRetries uint32
}

// SubscriptionEvent is returned when endpoints for a subscription have changed
type SubscriptionEvent struct {
	Error     error
	Type      EndpointsEventType
	Endpoints []Endpoint
}

// Subscription is a handle to a copperd subscription
type Subscription interface {
	// Endpoints returns a list of currently active endpoints
	Endpoints() ([]string, error)

	// Events subscribes to endpoint events and returns a channel
	Events() (<-chan SubscriptionEvent, error)

	// Open opens a stream to the service
	Open() (copper.Stream, error)

	// Close unsubscribes from the service
	Close() error
}

// PublishSettings describes the service, how far is copperd allowed to advertise
// it and how many concurrent streams an instance is able to handle.
type PublishSettings struct {
	Name        string
	Distance    uint32
	Concurrency uint32
}

// Publication is a handle to a copperd publication
type Publication interface {
	// Close unpublishes the service
	Close() error
}

// Server interface allows you to work with copperd servers
type Server interface {
	// Subscribe subscribes to a named service
	Subscribe(options ...SubscribeOption) (Subscription, error)

	// Publish publishes a named service
	Publish(settings PublishSettings, handler copper.StreamHandler) (Publication, error)

	// SetRoute sets a route on the server
	SetRoute(name string, routes ...Route) error

	// LookupRoute looks up a route on the server
	LookupRoute(name string) ([]Route, error)

	// Close closes the connection to the server
	Close() error
}

// LocalServer interface allows you to work with an in-process copperd server
type LocalServer interface {
	Server

	// AddPeer adds a peer to the server
	AddPeer(network, address string, distance int) error

	// AddUpstream adds an upstream to the server
	AddUpstream(network, address string) error

	// Listen listens on given network listeners, and returns when any of them
	// fail, or when Shutdown or Close are called
	Listen(listeners ...net.Listener) error

	// Shutdown shuts down a running server
	Shutdown() error
}
