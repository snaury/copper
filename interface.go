package copper

import (
	"net"
)

// Endpoint describes access endpoints for services
type Endpoint struct {
	Network  string
	Address  string
	TargetID int64
}

// SubscribeOption names the service and how far it is allowed to be
type SubscribeOption struct {
	Service     string
	MinDistance uint32
	MaxDistance uint32
}

// Route describes the target service for a route
type Route struct {
	Options []SubscribeOption
	Weight  uint32
}

// SubscribeSettings contains settings for the subscription
type SubscribeSettings struct {
	Options       []SubscribeOption
	MaxRetries    uint32
	DisableRoutes bool
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

// Subscription is a handle to a set of copper services
type Subscription interface {
	// Endpoints returns a list of currently active endpoints
	Endpoints() ([]Endpoint, error)

	// EndpointChanges returns a stream of endpoint changes
	EndpointChanges() (EndpointChangesStream, error)

	// Open opens a stream to an instance of a service
	Open() (Stream, error)

	// Stop unsubscribes from services
	Stop() error
}

// PublishSettings describes how far is copper allowed to advertise the
// service and how many concurrent streams an instance is able to handle.
type PublishSettings struct {
	Priority    uint32
	Distance    uint32
	Concurrency uint32
	QueueSize   uint32
}

// Publication is a handle to a copper publication
type Publication interface {
	// Stop unpublishes the service
	Stop() error
}

// ServiceChange is returned when service is added or changed
type ServiceChange struct {
	TargetID int64
	Name     string
	Settings PublishSettings
}

// ServiceChanges is returned with accumulated service changes
type ServiceChanges struct {
	Removed []int64
	Changed []ServiceChange
}

// ServiceChangesStream is a stream of service changes
type ServiceChangesStream interface {
	// Read returns the next service change
	Read() (ServiceChanges, error)
	// Stop stops listening for service changes
	Stop() error
}

// Client interface allows you to work with copper servers
type Client interface {
	// Subscribe subscribes to a named service
	Subscribe(settings SubscribeSettings) (Subscription, error)

	// Publish publishes a named service
	Publish(name string, settings PublishSettings, handler Handler) (Publication, error)

	// SetRoute sets a route on the server
	SetRoute(name string, routes ...Route) error

	// LookupRoute looks up a route on the server
	LookupRoute(name string) ([]Route, error)

	// ServiceChanges returns a stream of service changes
	ServiceChanges() (ServiceChangesStream, error)

	// RemoteAddr returns the remote address of the client
	RemoteAddr() net.Addr

	// Err returns an error that caused the client to be closed
	Err() error

	// Close closes the connection to the server
	Close() error

	// Done returns a channel that's closed when client is finished
	Done() <-chan struct{}

	// Closed returns a channel that's closed when client is closed
	Closed() <-chan struct{}

	// Shutdown stops accepting new streams and returns a channel that's closed
	// when all active handlers finish processing their requests. It does not
	// affect outgoing streams in any way, but it is recommended that the caller
	// unpublishes all services first, otherwise it might be very confusing for
	// clients to receive ECONNSHUTDOWN for requests to active publications.
	Shutdown() <-chan struct{}
}

// Server interface allows you to work with an in-process copper server
type Server interface {
	// AddPeer adds a peer to the server
	AddPeer(network, address string, distance uint32) error

	// Add a given network listener to the pool of listeners
	AddListener(listener net.Listener, allowChanges bool) error

	// Add a given network listener to the pool of http listeners
	AddHTTPListener(listener net.Listener) error

	// SetUpstream sets an upstream for the server
	SetUpstream(upstream Client) error

	// Err returns an error that caused the server to be closed
	Err() error

	// Close closes a running server
	Close() error

	// Done returns a channel that's closed when the server is finished
	Done() <-chan struct{}

	// Closed returns a channel that's closed when the server is closed
	Closed() <-chan struct{}
}
