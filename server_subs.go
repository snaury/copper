package copper

import (
	"fmt"
	"sort"
)

type serverPublicationByPriority []*serverPublication

func (p serverPublicationByPriority) Len() int      { return len(p) }
func (p serverPublicationByPriority) Swap(i, j int) { p[i], p[j] = p[j], p[i] }
func (p serverPublicationByPriority) Less(i, j int) bool {
	return p[i].settings.Priority < p[j].settings.Priority
}
func (p serverPublicationByPriority) Sort() { sort.Sort(p) }

type serverPeerRemotesByPriority []*serverPeerRemote

func (p serverPeerRemotesByPriority) Len() int      { return len(p) }
func (p serverPeerRemotesByPriority) Swap(i, j int) { p[i], p[j] = p[j], p[i] }
func (p serverPeerRemotesByPriority) Less(i, j int) bool {
	return p[i].settings.Priority < p[j].settings.Priority
}
func (p serverPeerRemotesByPriority) Sort() { sort.Sort(p) }

type serverSubscriptionOption struct {
	option *SubscribeOption
	route  *serverRoute
	local  serverPublicationByPriority
	remote serverPeerRemotesByPriority

	localPriority  uint32
	remoteCount    int
	remotePriority uint32
}

var _ endpointReference = &serverSubscriptionOption{}

func (o *serverSubscriptionOption) getEndpointsRLocked() []Endpoint {
	if o.route != nil {
		return o.route.getEndpointsRLocked()
	}
	if len(o.local) > 0 && o.localPriority <= o.remotePriority {
		return o.local[0].getEndpointsRLocked()
	}
	var result []Endpoint
	for _, remote := range o.remote {
		if remote.settings.Priority == o.remotePriority {
			result = append(result, remote.getEndpointsRLocked()...)
		}
	}
	return result
}

func (o *serverSubscriptionOption) handleRequestRLocked(callback handleRequestCallback) handleRequestStatus {
	if o.route != nil {
		return o.route.handleRequestRLocked(callback)
	}
	if len(o.local) > 0 && o.localPriority <= o.remotePriority {
		return o.local[0].handleRequestRLocked(callback)
	}
	if o.remoteCount > 0 {
		index := 0
		if o.remoteCount > 1 {
			index = globalRandom.Intn(o.remoteCount)
		}
		return o.remote[index].handleRequestRLocked(callback)
	}
	return handleRequestStatusNoRoute
}

func (o *serverSubscriptionOption) isActiveRLocked() bool {
	if o.route != nil {
		return true
	}
	if len(o.local) > 0 {
		return true
	}
	if len(o.remote) > 0 {
		return true
	}
	return false
}

func (o *serverSubscriptionOption) updateLocalPriorityLocked() {
	if len(o.local) > 0 {
		o.localPriority = o.local[0].settings.Priority
	} else {
		o.localPriority = 0xffffffff
	}
}

func (o *serverSubscriptionOption) updateRemotePriorityLocked() {
	if len(o.remote) > 0 {
		o.remoteCount = 1
		o.remotePriority = o.remote[0].settings.Priority
		for i := 1; i < len(o.remote); i++ {
			if o.remote[i].settings.Priority != o.remotePriority {
				break
			}
			o.remoteCount++
		}
	} else {
		o.remoteCount = 0
		o.remotePriority = 0xffffffff
	}
}

func (o *serverSubscriptionOption) addRouteLocked(route *serverRoute) bool {
	o.route = route
	return true
}

func (o *serverSubscriptionOption) removeRouteLocked(route *serverRoute) bool {
	if o.route == route {
		o.route = nil
		return true
	}
	return false
}

func (o *serverSubscriptionOption) addPublicationLocked(pub *serverPublication) bool {
	if o.option.MinDistance == 0 {
		// this option allows using local publications
		o.local = append(o.local, pub)
		return true
	}
	return false
}

func (o *serverSubscriptionOption) removePublicationLocked(pub *serverPublication) bool {
	if o.option.MinDistance == 0 {
		// this option allows using local publications
		for index, value := range o.local {
			if value == pub {
				copy(o.local[index:], o.local[index+1:])
				o.local[len(o.local)-1] = nil
				o.local = o.local[:len(o.local)-1]
				return true
			}
		}
	}
	return false
}

func (o *serverSubscriptionOption) addRemoteLocked(remote *serverPeerRemote) bool {
	if o.option.MinDistance <= remote.settings.Distance && remote.settings.Distance <= o.option.MaxDistance {
		// this remote falls under this option's filter
		o.remote = append(o.remote, remote)
		return true
	}
	return false
}

func (o *serverSubscriptionOption) removeRemoteLocked(remote *serverPeerRemote) bool {
	if o.option.MinDistance <= remote.settings.Distance && remote.settings.Distance <= o.option.MaxDistance {
		// this remote falls under this option's filter
		for index, value := range o.remote {
			if value == remote {
				copy(o.remote[index:], o.remote[index+1:])
				o.remote[len(o.remote)-1] = nil
				o.remote = o.remote[:len(o.remote)-1]
				return true
			}
		}
	}
	return false
}

type serverSubscription struct {
	owner    *server
	targetID int64
	settings SubscribeSettings

	options []serverSubscriptionOption
	active  int

	indexByName map[string][]int
}

var _ endpointReference = &serverSubscription{}

func (sub *serverSubscription) getEndpointsRLocked() []Endpoint {
	if sub.active < len(sub.options) {
		return sub.options[sub.active].getEndpointsRLocked()
	}
	// TODO: support upstream
	return nil
}

func (sub *serverSubscription) handleRequestRLocked(callback handleRequestCallback) handleRequestStatus {
	if sub.active < len(sub.options) {
		return sub.options[sub.active].handleRequestRLocked(callback)
	}
	// TODO: support upstream
	return handleRequestStatusNoRoute
}

func (sub *serverSubscription) isActiveRLocked() bool {
	return sub.active < len(sub.options)
}

func (sub *serverSubscription) updateActiveIndexLocked() {
	for index := range sub.options {
		if sub.options[index].isActiveRLocked() {
			sub.active = index
			return
		}
	}
	sub.active = len(sub.options)
}

func (sub *serverSubscription) addRouteLocked(route *serverRoute) {
	if sub.settings.DisableRoutes {
		return
	}
	changed := false
	for _, index := range sub.indexByName[route.name] {
		if sub.options[index].addRouteLocked(route) {
			changed = true
		}
	}
	if changed {
		route.subscriptions[sub] = struct{}{}
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) removeRouteLocked(route *serverRoute) {
	if sub.settings.DisableRoutes {
		return
	}
	changed := false
	for index := range sub.settings.Options {
		if sub.options[index].removeRouteLocked(route) {
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
		if sub.options[index].addPublicationLocked(pub) {
			changed = true
			sub.options[index].local.Sort()
			sub.options[index].updateLocalPriorityLocked()
		}
	}
	if changed {
		pub.subscriptions[sub] = struct{}{}
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) removePublicationLocked(pub *serverPublication) {
	changed := false
	for _, index := range sub.indexByName[pub.name] {
		if sub.options[index].removePublicationLocked(pub) {
			changed = true
			sub.options[index].updateLocalPriorityLocked()
		}
	}
	if changed {
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) addRemoteLocked(remote *serverPeerRemote) {
	changed := false
	for _, index := range sub.indexByName[remote.name] {
		if sub.options[index].addRemoteLocked(remote) {
			changed = true
			sub.options[index].remote.Sort()
			sub.options[index].updateRemotePriorityLocked()
		}
	}
	if changed {
		remote.subscriptions[sub] = struct{}{}
		sub.updateActiveIndexLocked()
	}
}

func (sub *serverSubscription) removeRemoteLocked(remote *serverPeerRemote) {
	changed := false
	for _, index := range sub.indexByName[remote.name] {
		if sub.options[index].removeRemoteLocked(remote) {
			changed = true
			sub.options[index].updateRemotePriorityLocked()
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

		options: make([]serverSubscriptionOption, len(settings.Options)),
		active:  len(settings.Options),

		indexByName: make(map[string][]int),
	}
	for index := range sub.settings.Options {
		option := &sub.settings.Options[index]
		sub.options[index].option = option
		sub.indexByName[option.Service] = append(sub.indexByName[option.Service], index)
	}
	for name, indexes := range sub.indexByName {
		// Let others know we have an interest in this name
		subs := sub.owner.subsByName[name]
		if subs == nil {
			subs = make(map[*serverSubscription]struct{})
			sub.owner.subsByName[name] = subs
		}
		subs[sub] = struct{}{}

		// Find an existing route, if any
		if !sub.settings.DisableRoutes {
			if route := sub.owner.routeByName[name]; route != nil {
				changed := false
				for _, index := range indexes {
					if sub.options[index].addRouteLocked(route) {
						changed = true
					}
				}
				if changed {
					route.subscriptions[sub] = struct{}{}
				}
			}
		}

		// Find and subscribe to local publications
		for _, pub := range sub.owner.pubsByName[name] {
			changed := false
			for _, index := range indexes {
				if sub.options[index].addPublicationLocked(pub) {
					changed = true
				}
			}
			if changed {
				pub.subscriptions[sub] = struct{}{}
			}
		}

		// Find and subscribe to remote publications
		for _, peer := range s.peers {
			for _, remote := range peer.remotesByName[name] {
				changed := false
				for _, index := range indexes {
					if sub.options[index].addRemoteLocked(remote) {
						changed = true
					}
				}
				if changed {
					remote.subscriptions[sub] = struct{}{}
				}
			}
		}
	}
	for index := range sub.options {
		sub.options[index].local.Sort()
		sub.options[index].updateLocalPriorityLocked()
		sub.options[index].remote.Sort()
		sub.options[index].updateRemotePriorityLocked()
	}
	// TODO: support upstream
	sub.updateActiveIndexLocked()
	return sub, nil
}

func (sub *serverSubscription) unsubscribeLocked() {
	// TODO: support upstream
	for index := range sub.options {
		o := &sub.options[index]
		if o.route != nil {
			delete(o.route.subscriptions, sub)
		}
		for _, pub := range o.local {
			delete(pub.subscriptions, sub)
		}
		for _, remote := range o.remote {
			delete(remote.subscriptions, sub)
		}
		o.route = nil
		o.local = nil
		o.remote = nil
	}
	for name := range sub.indexByName {
		if subs := sub.owner.subsByName[name]; subs != nil {
			delete(subs, sub)
			if len(subs) == 0 {
				delete(sub.owner.subsByName, name)
			}
		}
	}
	sub.active = 0
	sub.options = nil
	sub.indexByName = nil
}
