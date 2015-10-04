# Copper for microservices

Copper is a per-node service that simplifies communication between microservices across your datacenters. The idea is to allow applications to subscribe to arbitrary names (called options) and later use these subscriptions to optimistically open data streams to services that published themselves under one of those names. Copper solves this single problem and then gets out of your way!

## How subscriptions work

A client may open multiple subscriptions to other services over a single network connection. Each subscriptions consists of one or more options, each having a name and distance (minimum and maximum) to a published service.

When opening streams options work as if they are considered in order, and the first one that is able to connect wins. This allows implementing fallbacks, e.g. when subscribing to `service1`, `service2` connections to `service2` would only be attempted if `service1` is not available on any of the known nodes at the time.

Distances come into play when there are multiple nodes or even datacenters combined into a cloud. Currently distance 0 is reserved for local services, distance 1 is used for services on other nodes in the same datacenter, and distance 2 is used for services on nodes in a neighboring datacenter.

Python client supports simplified subscriptions without specifying distances (only a service name), in which case those are internally translated into two options: `(name, 0, 1)` and `(name, 2, 2)`. This should be read as follows: connect to the best service in my local datacenter, but if there are none consider connecting to a neighboring datacenter.

Streams in copper are very cheap, so the usual way of using subscriptions is to subscribe once to a given name and then to open a new stream for every request. However, in a rare cases where multiple requests need to be handled by the same process, then those requests should be sent over the same stream (provided the underlying protocol supports that).

## How publishing works

An aplication may publish multiple services over a single network connection. When publishing services one may also specify:

* Maximum concurrency (default 1) that an application can handle
* Maximum queue size (default 64) that is allowed in the local node before service capacity would overflow
* Maximum distance (default 2) where the service should be made available
* Priority of the publication (default 0)

Priorities may be used to discourage connections to a given instance of the service, for example default priority of 0 is the highest possible priority, and publications with priority 1 would only be considered if there are no services with priority 0 published in the distance range of the subscriptions. The intention is to support:

* "hot standby" applications, which are always on, but would only receive requests when all primaries stop being available in a datacenter
* "cold replica" applications, which may take a long time to spin up after the primary stops, but which should stop getting requests as soon as the primary is back up again

Such a rigid priority system is augmented by subscriptions with multiple options, where the default is optimized for connecting to services in a local datacenter (whichever priority), falling back to a neighboring datacenter only when necessary.

Multiple applications may publish services with the same name and priority, in which case incoming requests would be randomly distributed between processes. Concurrency and queue size are cumulative in that case.

## Optimistic connections

Streams in copper are opened optimistically. For example subscriptions almost never fail (except for obvious reasons like having no options or other programmer mistakes), even if it would be impossible to connect to any real service in the future. Similarly opening streams with subscriptions does not fail (except for obvious problems like connection to the local node failing at a very unfortunate time), which allows clients to immediately start buffering requests for the wire. However, if it is indeed impossible to connect to a real published service then either reads or (less likely) writes would eventually fail with an error.

Sometimes it is important to know if the recipient is actually able to receive data on the other side (e.g. before trying to stream a big file) copper supports waiting for stream acknowledgements. Streams are acknowledged automatically by higher-level clients before they are given to application handlers.

## Naming services

Service names are arbitrary, however it is suggested to name them in the `proto:name` format, where `proto` specifies which protocol the service speaks.

Protocol `http` is reserved for services that speak HTTP, and such services are also automatically exposed by the node as `http://localhost:5380/name/` or even as `http://localhost:5380/` (if `name` is specified in the `X-Copper-Service` header).

## Getting out of the way

Note that copper avoids forcing you to run your services in any particular way. It is not a framework, it's just a tool to simplify managing all those pipes between your applications and conserve resources. One of the simplest ways to publish a service is with a Python script running in a console. A more involved solution would be starting docker containers with a bind-mounted `/run/copper`.

Copper avoids adding constraints on protocols, services may speak any protocol they desire over a stream, it's just bytes as far as the node is concerned. Currently an outer layer is required (a protocol inspired by HTTP/2, optimized for simple data pipes, see [here](doc/PROTOCOL.md), plus protobuf based RPC messages for copper-node, which you may find [here](protocol/copper.proto)), but support for "external" services may be added if needed.

## Limitations

Currently each node is fully connected to all other nodes and each node synchronizes publication changes from other nodes separately. This seems to work well for small clusters, e.g. several datacenters with dozens of machines each, but I suspect it cannot efficiently scale to really big clouds. For a cloud of `N` nodes each node would have `N-1` incoming TCP connections and would open `N-1` outgoing TCP connections. By default on Linux there are roughly 28k ephemeral ports, which would translate to the same upper limit on the total number of nodes. However, given that all nodes subscribe to changes from all other nodes it may cause too much chatter when many applications are started and stopped across the cloud. This is not yet explored territory.

Support for "upstream" nodes is planned in the future, which may help with bringing the total number of connections down.
