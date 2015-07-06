package copper

import (
	"fmt"
)

type serverSubscription struct {
	owner    *server
	targetID int64
	settings SubscribeSettings

	indexByName    map[string][]int
	trackedPubs    []map[uint32]*serverPublication
	trackedRemotes []map[*serverPeerRemote]struct{}
	remotePriority []uint32

	routes  []*serverRoute
	locals  []*serverPublication
	remotes []map[*serverPeerRemote]struct{}
	active  int
}

var _ endpointReference = &serverSubscription{}

func (sub *serverSubscription) getEndpointsLocked() []Endpoint {
	if sub.active < len(sub.settings.Options) {
		if route := sub.routes[sub.active]; route != nil {
			return route.getEndpointsLocked()
		}
		if local := sub.locals[sub.active]; local != nil && local.settings.Priority <= sub.remotePriority[sub.active] {
			return local.getEndpointsLocked()
		}
		var result []Endpoint
		for remote := range sub.remotes[sub.active] {
			result = append(result, remote.getEndpointsLocked()...)
		}
		return result
	}
	// TODO: support upstream
	return nil
}

func (sub *serverSubscription) handleRequestLocked(client Stream) handleRequestStatus {
	if sub.active < len(sub.settings.Options) {
		if route := sub.routes[sub.active]; route != nil {
			return route.handleRequestLocked(client)
		}
		if local := sub.locals[sub.active]; local != nil && local.settings.Priority <= sub.remotePriority[sub.active] {
			return local.handleRequestLocked(client)
		}
		for remote := range sub.remotes[sub.active] {
			switch status := remote.handleRequestLocked(client); status {
			case handleRequestStatusNoRoute:
				continue
			case handleRequestStatusFailure:
				continue
			default:
				return status
			}
		}
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
		if sub.routes[index] != nil || sub.locals[index] != nil || len(sub.remotes[index]) > 0 {
			if sub.active > index {
				sub.active = index
				break
			}
		}
	}
}

func (sub *serverSubscription) updateRemotePriorityLocked(index int) {
	sub.remotePriority[index] = 0xffffffff
	for remote := range sub.trackedRemotes[index] {
		if sub.remotePriority[index] > remote.settings.Priority {
			sub.remotePriority[index] = remote.settings.Priority
		}
	}
	for remote := range sub.trackedRemotes[index] {
		if sub.remotePriority[index] == remote.settings.Priority {
			sub.remotes[index][remote] = struct{}{}
		}
	}
}

func (sub *serverSubscription) addRouteLocked(route *serverRoute) {
	if sub.settings.DisableRoutes {
		return
	}
	changed := false
	for _, index := range sub.indexByName[route.name] {
		route.subscriptions[sub] = struct{}{}
		sub.routes[index] = route
		changed = true
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
	for _, index := range sub.indexByName[pub.name] {
		option := sub.settings.Options[index]
		if option.MinDistance == 0 {
			pub.subscriptions[sub] = struct{}{}
			sub.trackedPubs[index][pub.settings.Priority] = pub
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
	for _, index := range sub.indexByName[pub.name] {
		delete(sub.trackedPubs[index], pub.settings.Priority)
		if sub.locals[index] == pub {
			sub.locals[index] = nil
			// Select a new lower priority publication
			for _, candidate := range sub.trackedPubs[index] {
				if old := sub.locals[index]; old == nil || candidate.settings.Priority < old.settings.Priority {
					sub.locals[index] = candidate
				}
			}
			changed = true
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) addRemoteLocked(remote *serverPeerRemote) {
	changed := false
	for _, index := range sub.indexByName[remote.name] {
		option := sub.settings.Options[index]
		if remote.settings.Distance >= option.MinDistance && remote.settings.Distance <= option.MaxDistance {
			// this remote falls under this option's filter
			remote.subscriptions[sub] = struct{}{}
			sub.trackedRemotes[index][remote] = struct{}{}
			if sub.remotePriority[index] > remote.settings.Priority {
				// a new lower priority remote, replace all current
				for r := range sub.remotes[index] {
					delete(sub.remotes[index], r)
				}
				sub.remotePriority[index] = remote.settings.Priority
				sub.remotes[index][remote] = struct{}{}
				changed = true
			} else if sub.remotePriority[index] == remote.settings.Priority {
				sub.remotes[index][remote] = struct{}{}
				changed = true
			}
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) removeRemoteLocked(remote *serverPeerRemote) {
	changed := false
	for _, index := range sub.indexByName[remote.name] {
		delete(sub.trackedRemotes[index], remote)
		if sub.remotePriority[index] == remote.settings.Priority {
			delete(sub.remotes[index], remote)
			if len(sub.remotes[index]) == 0 {
				sub.updateRemotePriorityLocked(index)
			}
			changed = true
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (s *server) subscribeLocked(settings SubscribeSettings) (*serverSubscription, error) {
	if len(settings.Options) == 0 {
		return nil, fmt.Errorf("cannot subscribe with 0 options")
	}
	sub := &serverSubscription{
		owner:    s,
		targetID: s.allocateTargetID(),
		settings: settings,

		indexByName:    make(map[string][]int),
		trackedPubs:    make([]map[uint32]*serverPublication, len(settings.Options)),
		trackedRemotes: make([]map[*serverPeerRemote]struct{}, len(settings.Options)),
		remotePriority: make([]uint32, len(settings.Options)),

		routes:  make([]*serverRoute, len(settings.Options)),
		locals:  make([]*serverPublication, len(settings.Options)),
		remotes: make([]map[*serverPeerRemote]struct{}, len(settings.Options)),
		active:  len(settings.Options),
	}
	for index, option := range sub.settings.Options {
		sub.indexByName[option.Service] = append(sub.indexByName[option.Service], index)
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
			sub.trackedPubs[index] = make(map[uint32]*serverPublication)
			for _, pub := range sub.owner.pubsByName[option.Service] {
				pub.subscriptions[sub] = struct{}{}
				sub.trackedPubs[index][pub.settings.Priority] = pub
				if old := sub.locals[index]; old == nil || pub.settings.Priority < old.settings.Priority {
					sub.locals[index] = pub
				}
			}
		}
		sub.trackedRemotes[index] = make(map[*serverPeerRemote]struct{})
		for _, peer := range s.peers {
			for _, remote := range peer.remotesByName[option.Service] {
				if remote.settings.Distance >= option.MinDistance && remote.settings.Distance <= option.MaxDistance {
					remote.subscriptions[sub] = struct{}{}
					sub.trackedRemotes[index][remote] = struct{}{}
				}
			}
		}
		sub.remotes[index] = make(map[*serverPeerRemote]struct{})
		sub.updateRemotePriorityLocked(index)
	}
	// TODO: support upstream
	sub.updateActiveIndexLocked()
	return sub, nil
}

func (sub *serverSubscription) unsubscribeLocked() {
	// TODO: support upstream
	for index, option := range sub.settings.Options {
		// TODO: support remote services
		for _, pub := range sub.trackedPubs[index] {
			delete(pub.subscriptions, sub)
		}
		if route := sub.routes[index]; route != nil {
			delete(route.subscriptions, sub)
		}
		sub.trackedPubs[index] = nil
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
