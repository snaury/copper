package copperd

import (
	"sync"
)

type lowLevelServer interface {
	Server
	subscribe(options ...SubscribeOption) (int64, error)
	getEndpoints(targetID int64) ([]Endpoint, error)
	streamEndpoints(targetID int64) (EndpointChangesStream, error)
	unsubscribe(targetID int64) error
	publish(targetID int64, settings PublishSettings) error
	unpublish(targetID int64) error
}

type server struct {
	lock sync.Mutex
}
