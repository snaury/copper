package copperd

import (
	"net"

	"github.com/snaury/copper"
)

// Route describes the target service for a route
type Route struct {
	Service  string
	Weight   uint32
	Distance uint32
}

// Endpoint describes access endpoints for services
type Endpoint struct {
	Network  string
	Address  string
	TargetID int64
}

// SubscribeOption names the service and how far it is allowed to be
type SubscribeOption struct {
	Service  string
	Distance uint32
}

// SubscribeSettings contains settings for the subscription
type SubscribeSettings struct {
	Options    []SubscribeOption
	MaxRetries uint32
}

// EndpointChanges is returned when endpoints for a subscription have changed
type EndpointChanges struct {
	Added   []Endpoint
	Removed []Endpoint
}

// EndpointChangesStream is a stream of endpoint changes
type EndpointChangesStream interface {
	// Read returns the next endpoint changes
	Read() (EndpointChanges, error)

	// Stop stops listening for endpoint changes
	Stop() error
}

// Subscription is a handle to a set of copperd services
type Subscription interface {
	// Endpoints returns a list of currently active endpoints
	Endpoints() ([]Endpoint, error)

	// EndpointChanges returns a stream of endpoint changes
	EndpointChanges() (EndpointChangesStream, error)

	// Open opens a stream to an instance of a service
	Open() (copper.Stream, error)

	// Stop unsubscribes from services
	Stop() error
}

// PublishSettings describes how far is copperd allowed to advertise the
// service and how many concurrent streams an instance is able to handle.
type PublishSettings struct {
	Distance    uint32
	Concurrency uint32
}

// Publication is a handle to a copperd publication
type Publication interface {
	// Stop unpublishes the service
	Stop() error
}

// ServiceChange is returned when service is added or removed on the daemon
type ServiceChange struct {
	TargetID    int64
	Name        string
	Distance    uint32
	Concurrency uint32
}

// ServiceChangeStream is a stream of service changes
type ServiceChangeStream interface {
	// Read returns the next service change
	Read() (ServiceChange, error)
	// Stop stops listening for service changes
	Stop() error
}

// Client interface allows you to work with copperd servers
type Client interface {
	// Subscribe subscribes to a named service
	Subscribe(settings SubscribeSettings) (Subscription, error)

	// Publish publishes a named service
	Publish(name string, settings PublishSettings, handler copper.StreamHandler) (Publication, error)

	// SetRoute sets a route on the server
	SetRoute(name string, routes ...Route) error

	// LookupRoute looks up a route on the server
	LookupRoute(name string) ([]Route, error)

	// ServiceChanges returns a stream of service changes
	ServiceChanges() (ServiceChangeStream, error)

	// Close closes the connection to the server
	Close() error
}

// Server interface allows you to work with an in-process copperd server
type Server interface {
	// AddPeer adds a peer to the server
	AddPeer(network, address string, distance uint32) error

	// AddUpstream adds an upstream to the server
	AddUpstream(network, address string) error

	// Add given network listeners to the pool of listeners
	AddListeners(listeners ...net.Listener) error

	// Shutdown shuts down a running server
	Shutdown() error

	// Serve runs the server until it is shut down
	Serve() error
}
