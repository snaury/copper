package copperd

import (
	"fmt"

	"github.com/snaury/copper/raw"
)

type serverSubscription struct {
	owner    *server
	targetID int64
	settings SubscribeSettings

	tracked []map[uint32]*serverPublication

	routes []*serverRoute
	locals []*serverPublication
	active int
}

var _ endpointReference = &serverSubscription{}

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

func (sub *serverSubscription) handleRequestLocked(client raw.Stream) handleRequestStatus {
	if sub.active < len(sub.settings.Options) {
		if route := sub.routes[sub.active]; route != nil {
			return route.handleRequestLocked(client)
		}
		if local := sub.locals[sub.active]; local != nil {
			return local.handleRequestLocked(client)
		}
		// TODO: support remote services
	}
	// TODO: support upstream
	return handleRequestStatusNoRoute
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
			sub.tracked[index][pub.settings.Priority] = pub
			if old := sub.locals[index]; old == nil || pub.settings.Priority < old.settings.Priority {
				sub.locals[index] = pub
				changed = true
			}
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) removePublicationLocked(pub *serverPublication) {
	changed := false
	for index, option := range sub.settings.Options {
		if option.MinDistance == 0 && option.Service == pub.name {
			delete(sub.tracked[index], pub.settings.Priority)
			if sub.locals[index] == pub {
				sub.locals[index] = nil
				// Select a new lower priority publication
				for _, candidate := range sub.tracked[index] {
					if old := sub.locals[index]; old == nil || candidate.settings.Priority < old.settings.Priority {
						sub.locals[index] = candidate
					}
				}
				changed = true
			}
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) addRemoteLocked(remote *serverPeerRemote) {
	// TODO
}

func (sub *serverSubscription) updateRemoteLocked(remote *serverPeerRemote) {
	// TODO
}

func (sub *serverSubscription) removeRemoteLocked(remote *serverPeerRemote) {
	// TODO
}

func (s *server) subscribeLocked(settings SubscribeSettings) (*serverSubscription, error) {
	if len(settings.Options) == 0 {
		return nil, fmt.Errorf("cannot subscribe with 0 options")
	}
	sub := &serverSubscription{
		owner:    s,
		targetID: s.allocateTargetID(),
		settings: settings,

		tracked: make([]map[uint32]*serverPublication, len(settings.Options)),

		routes: make([]*serverRoute, len(settings.Options)),
		locals: make([]*serverPublication, len(settings.Options)),
		active: len(settings.Options),
	}
	for index, option := range sub.settings.Options {
		// Let others know we have an interested in this name
		subs := sub.owner.subsByName[option.Service]
		if subs == nil {
			subs = make(map[*serverSubscription]struct{})
			sub.owner.subsByName[option.Service] = subs
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
			sub.tracked[index] = make(map[uint32]*serverPublication)
			for _, pub := range sub.owner.pubsByName[option.Service] {
				pub.subscriptions[sub] = struct{}{}
				sub.tracked[index][pub.settings.Priority] = pub
				if old := sub.locals[index]; old == nil || pub.settings.Priority < old.settings.Priority {
					sub.locals[index] = pub
				}
			}
		}
		// TODO: support remote services
	}
	// TODO: support upstream
	sub.updateActiveIndexLocked()
	return sub, nil
}

func (sub *serverSubscription) unsubscribeLocked() {
	// TODO: support upstream
	for index, option := range sub.settings.Options {
		// TODO: support remote services
		for _, pub := range sub.tracked[index] {
			delete(pub.subscriptions, sub)
		}
		if route := sub.routes[index]; route != nil {
			delete(route.subscriptions, sub)
		}
		sub.tracked[index] = nil
		sub.locals[index] = nil
		sub.routes[index] = nil
		if subs := sub.owner.subsByName[option.Service]; subs != nil {
			delete(subs, sub)
			if len(subs) == 0 {
				delete(sub.owner.subsByName, option.Service)
			}
		}
	}
	sub.active = len(sub.settings.Options)
}
