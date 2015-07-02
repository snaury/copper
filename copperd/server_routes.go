package copperd

import (
	"fmt"

	"github.com/snaury/copper"
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

func (r *serverRoute) open() (copper.Stream, error) {
	return nil, ErrUnsupported
}

func (r *serverRoute) decref() bool {
	return false
}

func (r *serverRoute) getEndpointsLocked() []Endpoint {
	var result []Endpoint
	for _, c := range r.cases {
		if c.weight > 0 && c.sub.isActiveLocked() {
			result = append(result, c.sub.getEndpointsLocked()...)
		}
	}
	return result
}

func (r *serverRoute) selectEndpointLocked() (endpointReference, error) {
	sum := int64(0)
	for _, c := range r.cases {
		if c.weight > 0 && c.sub.isActiveLocked() {
			sum += int64(c.weight)
		}
	}
	if sum == 0 {
		// There are no routes
		return nil, copper.ENOROUTE
	}
	bin := r.owner.random.Int63n(sum)
	for _, c := range r.cases {
		if c.weight > 0 && c.sub.isActiveLocked() {
			if bin < int64(c.weight) {
				return c.sub.selectEndpointLocked()
			}
			bin -= int64(c.weight)
		}
	}
	// this should never happen, might replace it with a panic!
	return nil, fmt.Errorf("random number %d didn't match the %d sum", bin, sum)
}

func (s *server) setRouteLocked(name string, routes []Route) error {
	r := s.routeByName[name]
	if r == nil {
		if len(routes) == 0 {
			return nil
		}
		r = &serverRoute{
			owner:  s,
			name:   name,
			routes: routes,

			subscriptions: make(map[*serverSubscription]struct{}),
		}
		s.routeByName[name] = r
		if subs := s.subByName[name]; subs != nil {
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
			c.sub.unregisterLocked()
		}
		r.cases = nil
		r.routes = nil
		delete(s.routeByName, name)
		return nil
	} else {
		for _, c := range r.cases {
			c.sub.unregisterLocked()
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
	}
	return nil
}
