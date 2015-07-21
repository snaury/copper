package copper

import (
	"fmt"
	"reflect"
)

type serverRouteCase struct {
	sub    *serverSubscription
	weight uint32
}

type serverRoute struct {
	owner  *server
	name   string
	routes []Route

	cases []serverRouteCase

	subscriptions map[*serverSubscription]struct{}
}

var _ endpointReference = &serverRoute{}

func (r *serverRoute) getEndpointsLocked() []Endpoint {
	var result []Endpoint
	for _, c := range r.cases {
		if c.weight > 0 && c.sub.isActiveLocked() {
			result = append(result, c.sub.getEndpointsLocked()...)
		}
	}
	return result
}

func (r *serverRoute) handleRequestLocked(callback handleRequestCallback) handleRequestStatus {
	sum := int64(0)
	for _, c := range r.cases {
		if c.weight > 0 && c.sub.isActiveLocked() {
			sum += int64(c.weight)
		}
	}
	if sum == 0 {
		// There are no routes
		return handleRequestStatusNoRoute
	}
	bin := globalRandom.Int63n(sum)
	for _, c := range r.cases {
		if c.weight > 0 && c.sub.isActiveLocked() {
			if bin < int64(c.weight) {
				return c.sub.handleRequestLocked(callback)
			}
			bin -= int64(c.weight)
		}
	}
	// this should never happen!
	panic(fmt.Errorf("random number %d didn't match the sum %d", bin, sum))
}

func (s *server) setRouteLocked(name string, routes []Route) error {
	r := s.routeByName[name]
	if r == nil {
		if len(routes) == 0 {
			// Don't do anything if this call is trying to drop the route
			return nil
		}
		r = &serverRoute{
			owner:  s,
			name:   name,
			routes: routes,

			subscriptions: make(map[*serverSubscription]struct{}),
		}
		s.routeByName[name] = r
		if subs := s.subsByName[name]; subs != nil {
			for sub := range subs {
				sub.addRouteLocked(r)
			}
		}
	} else if len(routes) == 0 {
		for sub := range r.subscriptions {
			sub.removeRouteLocked(r)
		}
		r.subscriptions = nil
		for _, c := range r.cases {
			c.sub.unsubscribeLocked()
		}
		r.cases = nil
		r.routes = nil
		delete(s.routeByName, name)
		return nil
	} else {
		if reflect.DeepEqual(r.routes, routes) {
			// Don't do anything if routes didn't actually change
			return nil
		}
		for _, c := range r.cases {
			c.sub.unsubscribeLocked()
		}
		r.cases = nil
		r.routes = routes
	}
	// we need to build our cases
	r.cases = make([]serverRouteCase, len(r.routes))
	for index, route := range r.routes {
		sub, err := s.subscribeLocked(SubscribeSettings{
			Options:       route.Options,
			MaxRetries:    1,
			DisableRoutes: true,
		})
		if err != nil {
			panic(fmt.Errorf("unexpected subscribe error: %s", err))
		}
		r.cases[index].sub = sub
		r.cases[index].weight = route.Weight
	}
	return nil
}
