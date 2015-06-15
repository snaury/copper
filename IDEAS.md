# Coping with Microservices

The basic idea is to make communication between microservices in a cloud of clusters in different datacenters as simple as possible, making launching and debugging microservices easier, moving routing and load balancing out of scope of application code.

Instead all those tasks would be handled by a coperd daemon on every machine.

There are several problems such a daemon has to solve.

## Service Discovery

I think it is not beneficial for applications to perform service discovery and load balancing in the trivial case of making outgoing requests. Applications in a cloud of microservices usually only need to make requests to other services and receive a reply.

Suppose an application performs service discovery in its own process. This entails looking up endpoints, maintaining pools of connections, sending and receiving heartbeats, load balancing between instances of other services, and those are all problems that are very hard to get right. Even more so for services implemented in scripting languages (e.g. Python or Perl): scaling can be achieved by spawning more processes, however significant resources are needed for service discovery and load balancing (memory, connections, ports), which cannot be shared between processes, so it becomes a back-pressure that prevents scaling beyond some number of processes.

It might be better if service discovery, connection pooling and load balancing is performed by a per-machine daemon that would do it correctly and efficiently. Applications would connect to such a daemon and simply multiplex requests to other services.

## Outgoing requests

A high level sequence may be like this:

    SUBSCRIBE {service-id} {service}
    SUBSCRIBED {service-id}
    EVENT {service-id} {data}...

    OPEN {service-id} {data}...
    DATA {data}...
    RESET {reason}

    UNSUBSCRIBE {service-id}
    UNSUBSCRIBED {service-id}

SUBSCRIBE would assign a service-id for future reference to service (so as not to repeat the same service name with every request), and optionally subscribes to updates about that service, which are delivered as events, and would contain basic or detailed information about available endpoints that can handle requests for that service directly if needed.

Outgoing OPEN would open a stream to some instance of a service, with further data streamed in DATA packets in both directions. The protocol for that data would not be specified, and could theoretically be anything, e.g. a tunneled HTTP/1.1 connection. The tunnel for communication would be routed based on scope flags (e.g. strictly local, any instance in a datacenter, any instance in any datacenter, etc.). Data may be written until end of stream is specified in flags, in which case stream becomes half-closed, and RESET would be used to forcibly close both sides of a stream and cancel a pending request.

Channels opened with OPEN in such a scheme would be very cheap, and in its simplest form (for short requests) it would take a single outgoing OPEN packet containing both request data and an end-of-stream flag, where an incoming DATA packet has both response data and an end-of-stream flag.

It would be even better if higher level logic such as SUBSCRIBE and EVENT were simple messages over a special stream 0.

## Service Publishing

Coperd should not manage applications in its simplest form, but any local application should be able to connect to the local daemon and register its (possibly multiple) services. Multiple local processes should be able to register the same service, which coperd would use for load balancing. The sequence would probably be like this:

    PUBLISH {service} {service-id}
    PUBLISHED {service-id}

    OPEN {service-id} {data}...
    DATA {data}...
    RESET {reason}

    UNPUBLISH {service-id}
    UNPUBLISHED {service-id}

PUBLISH would assign a service-id for future reference to service and make it available to the cloud. Additional settings might be concurrency (how many simultaneous requests instance can handle), priority (for backup instances to be used only in case primary instances go down), availability to other machines or datacenters, etc.

Incoming OPEN would open a stream to a service, which application would handle according to some application-specific protocol, or RESET it for some reason.

UNPUBLISH would unpublish a service and stop new requests from coming in (after it is confirmed with an UNPUBLISHED message), but would not cancel any streams that are already open, which would make graceful shutdown and restart trivial to implement.

## Communication between nodes

Coperd would be using the same basic protocol when communicating with each other, except local delivery will always be specified in any subscriptions to prevent loops or non-direct paths. Actual service registrations may be stored in zookeeper (similar to finagle), however it may be much easier if nodes discover them on their own (which might improve reaction times to network problems). Any node would be able to determine which endpoints serve a specific service and connect to them directly. I expect there to be two connections between any two coper daemons (when bidirectional requests are needed), which probably wouldn't scale for too many machines, but would be simple to start with.

Coperd would initially discover themselves with zookeeper, though it would be important that the system continues to operate even when zookeeper is not available.

## Low-level protocol

Inspired by SPDY the protocol should support the following:

* Multiplexing of multiple streams
* Flow control for memory-constrained proxying and back-pressure
* Daemon should not really be special and should use the same mechanisms as everyone else for internal requests

Similar to SPDY data frames would have the following format:

```
+-----------------------+
|0| stream_id (31 bits) |
|-----------------------|
| flags (8) | size (24) |
+-----------------------+
```

And control frames would have the following format:

```
+-----------------------+
|1| reserved | type (8) |
|-----------------------|
| flags (8) | size (24) |
+-----------------------+
```

The general idea is that these are two little-endian uint32_t numbers, so a header always has a fixed size, and packet boundary can be extracted from the second number. The size includes all bytes that follow the header.

For stream_id there are the following rules:

* stream_id 0 is special and has a meaning of the whole connection. Any data frames would communicate with a connection owner (e.g. coperd would expect coper messages on that stream). An end-of-stream flag on stream 0 is a request to close the connection as gracefully as possible (with a rule that new streams cannot be opened by the side initiating a graceful close of the connection).
* stream_id generated by the client must be odd
* stream_id generated by the server must be even

There are not many packet types so far:

* (0) SETTINGS for settings (window sizes, etc.)
* (1) WINDOW for updating a receive window
* (2) OPEN for opening new streams
* (3) RESET for resetting open streams
* (4) PING for pings and pongs
* (5) FATAL for fatal connection-level errors

Unlike SPDY or HTTP/2 there are no headers and actual stream data is as abstract as possible.

### Frame type 0: SETTINGS

TODO

### Frame type 1: WINDOW

TODO

### Frame type 2: OPEN

TODO

### Frame type 3: RESET

TODO

### Frame type 4: PING

TODO

### Frame type 5: FATAL

TODO
