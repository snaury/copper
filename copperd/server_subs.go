package copperd

import (
	"fmt"

	"github.com/snaury/copper"
)

type serverSubscription struct {
	owner    *server
	targetID int64
	settings SubscribeSettings

	routes []*serverRoute
	locals []*serverPublication
	active int
}

var _ endpointReference = &serverSubscription{}

func (sub *serverSubscription) open() (copper.Stream, error) {
	return nil, ErrUnsupported
}

func (sub *serverSubscription) decref() bool {
	return false
}

func (sub *serverSubscription) getEndpointsLocked() []Endpoint {
	if sub.active < len(sub.settings.Options) {
		if route := sub.routes[sub.active]; route != nil {
			return route.getEndpointsLocked()
		}
		if local := sub.locals[sub.active]; local != nil {
			return local.getEndpointsLocked()
		}
		// TODO: support remote services
	}
	// TODO: support upstream
	return nil
}

func (sub *serverSubscription) selectEndpointLocked() (endpointReference, error) {
	if sub.active < len(sub.settings.Options) {
		if route := sub.routes[sub.active]; route != nil {
			return route.selectEndpointLocked()
		}
		if local := sub.locals[sub.active]; local != nil {
			return local.selectEndpointLocked()
		}
		// TODO: support remote services
	}
	// TODO: support upstream
	return nil, copper.ENOROUTE
}

func (sub *serverSubscription) isActiveLocked() bool {
	return sub.active < len(sub.settings.Options)
}

func (sub *serverSubscription) updateActiveIndexLocked() {
	sub.active = len(sub.settings.Options)
	for index := range sub.settings.Options {
		if sub.routes[index] != nil || sub.locals[index] != nil {
			if sub.active > index {
				sub.active = index
				break
			}
		}
		// TODO: support remote services
	}
}

func (sub *serverSubscription) addRouteLocked(route *serverRoute) {
	if sub.settings.DisableRoutes {
		return
	}
	changed := false
	for index, option := range sub.settings.Options {
		if option.Service == route.name {
			route.subscriptions[sub] = struct{}{}
			sub.routes[index] = route
			changed = true
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) removeRouteLocked(route *serverRoute) {
	if sub.settings.DisableRoutes {
		return
	}
	changed := false
	for index := range sub.settings.Options {
		if sub.routes[index] == route {
			sub.routes[index] = nil
			changed = true
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) addPublicationLocked(pub *serverPublication) {
	changed := false
	for index, option := range sub.settings.Options {
		if option.MinDistance == 0 && option.Service == pub.name {
			// This option allows local services and matches publication name
			pub.subscriptions[sub] = struct{}{}
			sub.locals[index] = pub
			changed = true
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) removePublicationLocked(pub *serverPublication) {
	changed := false
	for index := range sub.settings.Options {
		if sub.locals[index] == pub {
			sub.locals[index] = nil
			changed = true
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) registerLocked() {
	changed := false
	for index, option := range sub.settings.Options {
		// Let others know we have an interested in this name
		subs := sub.owner.subByName[option.Service]
		if subs == nil {
			subs = make(map[*serverSubscription]struct{})
			sub.owner.subByName[option.Service] = subs
		}
		subs[sub] = struct{}{}
		if !sub.settings.DisableRoutes {
			route := sub.owner.routeByName[option.Service]
			if route != nil {
				sub.routes[index] = route
				route.subscriptions[sub] = struct{}{}
			}
		}
		if option.MinDistance == 0 {
			// This option allows local services, look them up
			pub := sub.owner.pubByName[option.Service]
			if pub != nil {
				pub.subscriptions[sub] = struct{}{}
				sub.locals[index] = pub
				changed = true
			}
		}
		// TODO: support remote services
	}
	// TODO: support upstream
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) unregisterLocked() {
	// TODO: support upstream
	for index, option := range sub.settings.Options {
		// TODO: support remote services
		if pub := sub.locals[index]; pub != nil {
			sub.locals[index] = nil
			delete(pub.subscriptions, sub)
		}
		if route := sub.routes[index]; route != nil {
			sub.routes[index] = nil
			delete(route.subscriptions, sub)
		}
		if subs := sub.owner.subByName[option.Service]; subs != nil {
			delete(subs, sub)
			if len(subs) == 0 {
				delete(sub.owner.subByName, option.Service)
			}
		}
	}
	sub.active = len(sub.settings.Options)
}

func (s *server) subscribeLocked(settings SubscribeSettings) (*serverSubscription, error) {
	if len(settings.Options) == 0 {
		return nil, fmt.Errorf("cannot subscribe with 0 options")
	}
	sub := &serverSubscription{
		owner:    s,
		targetID: s.allocateTargetID(),
		settings: settings,

		routes: make([]*serverRoute, len(settings.Options)),
		locals: make([]*serverPublication, len(settings.Options)),
		active: len(settings.Options),
	}
	sub.registerLocked()
	return sub, nil
}
