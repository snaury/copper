# -*- coding: utf-8 -*-
import sys
import struct
from gevent import spawn, sleep
from gevent.socket import (
    socket,
    getaddrinfo,
    AF_UNIX,
    SOCK_STREAM,
    IPPROTO_TCP,
    TCP_NODELAY,
)
from .errors import (
    NoTargetError,
    ConnectionClosedError,
)
from .rawconn import (
    Condition,
    RawConn,
)
from .copper_pb2 import (
    SubscribeOption,
    Subscribe, SubscribeRequest, SubscribeResponse,
    GetEndpoints, GetEndpointsRequest, GetEndpointsResponse,
    Unsubscribe, UnsubscribeRequest, UnsubscribeResponse,
    Publish, PublishRequest, PublishResponse,
    Unpublish, UnpublishRequest, UnpublishResponse,
)

def _split_hostport(hostport):
    index = hostport.rfind(':')
    if index < 0:
        raise ValueError('invalid host/port: %r' % (hostport,))
    host, port = hostport[:index], hostport[index+1:]
    try:
        port = int(port)
    except ValueError:
        raise ValueError('invalid port: %r' % (hostport,))
    if host.startswith('[') and host.endswith(']'):
        host = host[1:-1]
    elif ':' in host or '[' in host or ']' in host:
        raise ValueError('invalid host: %r' % (hostport,))
    return host, port

def _validate_endpoint(endpoint):
    if not isinstance(endpoint, (list, tuple)) or len(endpoint) != 2:
        raise ValueError('invalid endpoint: %r' % (endpoint,))
    net, addr = endpoint
    if net in ('unix',):
        if not isinstance(addr, basestring):
            raise ValueError('invalid endpoint: %r' % (endpoint,))
        return ('unix', addr)
    if net in ('tcp',):
        if isinstance(addr, basestring):
            host, port = _split_hostport(addr)
        elif isinstance(addr, (list, tuple)) and len(addr) == 2:
            host, port = addr
            if not isinstance(host, basestring) or isinstance(port, (int, long)):
                raise ValueError('invalid endpoint: %r' % (endpoint,))
        else:
            raise ValueError('invalid endpoint: %r' % (endpoint,))
        return ('tcp', (host, port))
    raise ValueError('invalid endpoint: %r' % (endpoint,))

def _do_connect(endpoint):
    net, addr = endpoint
    if net in ('unix',):
        is_tcp = False
        addrinfo = [(AF_UNIX, SOCK_STREAM, 0, '', addr)]
    else:
        is_tcp = True
        addrinfo = getaddrinfo(addr[0], addr[1], 0, SOCK_STREAM)
    exc = None
    for family, socktype, proto, canonname, address in addrinfo:
        sock = socket(family, socktype, proto)
        try:
            sock.connect(address)
        except:
            exc = sys.exc_info()[1]
            continue
        if is_tcp:
            sock.setsockopt(IPPROTO_TCP, TCP_NODELAY, 1)
        return sock
    if exc is None:
        raise ValueError('no addresses for %r' % (endpoint,))
    raise exc

class CopperClient(object):
    class Subscription(object):
        def __init__(self, owner):
            self._owner = owner
            self._target_id = None

        def __enter__(self):
            return self

        def __exit__(self, exc_type=None, exc_val=None, exc_tb=None):
            self.stop()

        @property
        def endpoints(self):
            if self._target_id is None or self._owner._conn is None:
                return []
            request = GetEndpointsRequest()
            request.target_id = self._target_id
            response = self._owner._make_simple_request(GetEndpoints, request, GetEndpointsResponse)
            return [
                (endpoint.network, endpoint.address, endpoint.target_id)
                for endpoint in response.endpoints
            ]

        def open(self):
            self._owner.wait_connected()
            if self._target_id is None:
                raise RuntimeError('client cannot open streams at this time')
            return self._owner._conn.open(self._target_id)

        def stop(self):
            self._owner._subscriptions.pop(self, None)
            if self._target_id is None:
                return
            target_id = self._target_id
            self._target_id = None
            if target_id is None or self._owner._conn is None:
                return
            request = UnsubscribeRequest()
            request.target_id = target_id
            self._owner._make_simple_request(Unsubscribe, request, UnsubscribeResponse)

    class Publication(object):
        def __init__(self, owner, target_id):
            self._owner = owner
            self._target_id = target_id

        def __enter__(self):
            return self

        def __exit__(self, exc_type=None, exc_val=None, exc_tb=None):
            self.stop()

        def stop(self):
            self._owner._publications.pop(self, None)
            target_id = self._target_id
            self._target_id = None
            if target_id is None:
                return
            try:
                if self._owner._conn is not None:
                    request = UnpublishRequest()
                    request.target_id = target_id
                    self._owner._make_simple_request(Unpublish, request, UnpublishResponse)
            finally:
                self._owner._handlers.pop(target_id, None)

    def __init__(self, endpoint):
        self._conn = None
        self._closed = False
        self._shutdown = False
        self._endpoint = _validate_endpoint(endpoint)
        self._connected_cond = Condition()
        self._next_target_id = 1
        self._subscriptions = {}
        self._publications = {}
        self._handlers = {}
        spawn(self._connect_loop)

    def __enter__(self):
        return self

    def __exit__(self, exc_type=None, exc_val=None, exc_tb=None):
        self.close()

    def _connect_loop(self):
        while not self._closed and not self._shutdown:
            try:
                sock = _do_connect(self._endpoint)
            except:
                import traceback
                traceback.print_exc()
                if not self._closed:
                    sleep(5)
                continue
            conn = RawConn(sock, self._handle_stream)
            try:
                try:
                    self._reregister(conn)
                    self._conn = conn
                    self._connected_cond.broadcast()
                    # Wait until connection is closed
                    conn.wait_closed()
                finally:
                    self._conn = None
                    for sub in self._subscriptions:
                        sub._target_id = None
                raise conn.error
            except ConnectionClosedError:
                pass
            except:
                import traceback
                traceback.print_exc()

    def _reregister(self, conn):
        from gevent import wait
        def reregister_subscription(sub, request):
            response = self._make_simple_request(Subscribe, request, SubscribeResponse, conn)
            sub._target_id = response.target_id
        def reregister_publication(pub, request):
            self._make_simple_request(Publish, request, PublishResponse, conn)
        gg = []
        for sub, request in self._subscriptions.iteritems():
            gg.append(spawn(reregister_subscription, sub, request))
        for pub, request in self._publications.iteritems():
            gg.append(spawn(reregister_publication, pub, request))
        for g in wait(gg):
            g.get()

    FMT_REQTYPE = struct.Struct('<B')
    FMT_MSGSIZE = struct.Struct('<I')

    def _make_simple_request(self, request_type, request, response_class, conn=None):
        if conn is None:
            conn = self._conn
        request_data = request.SerializeToString()
        with conn.open(0) as stream:
            stream.write(self.FMT_REQTYPE.pack(request_type))
            stream.write(self.FMT_MSGSIZE.pack(len(request_data)))
            if request_data:
                stream.write(request_data)
            stream.close_write()
            response_size, = self.FMT_MSGSIZE.unpack(stream.read(4))
            if response_size > 0:
                response_data = stream.read(response_size)
            else:
                response_data = ''
        return response_class.FromString(response_data)

    def _handle_stream(self, stream):
        handler = self._handlers.get(stream.target_id)
        if handler is None:
            raise NoTargetError()
        handler(stream)

    def wait_connected(self):
        while not self._closed and self._conn is None and not self._shutdown:
            self._connected_cond.wait()
        if self._closed:
            raise RuntimeError('connection is closed')
        if self._conn is None and self._shutdown:
            raise RuntimeError('connection has been shut down')

    def close(self):
        if not self._closed:
            self._closed = True
            self._connected_cond.broadcast()
            if self._conn is not None:
                self._conn.close()

    def shutdown(self):
        self._shutdown = True
        if self._conn is None:
            self._connected_cond.broadcast()
        else:
            self._conn.shutdown()

    def subscribe(self, *args, **kwargs):
        if len(args) == 0:
            raise TypeError('subscribe needs at least one option')
        if len(args) == 1 and isinstance(args[0], basestring):
            min_distance = kwargs.pop('min_distance', None)
            max_distance = kwargs.pop('max_distance', None)
            if min_distance is None and max_distance is None:
                options = [
                    (args[0], 0, 1),
                    (args[0], 2, 2),
                ]
            else:
                if min_distance is None:
                    min_distance = 0
                elif max_distance is None:
                    max_distance = 2
                options = [
                    (args[0], min_distance, max_distance),
                ]
        else:
            options = args
        max_retries = kwargs.pop('max_retries', None)
        disable_routes = kwargs.pop('disable_routes', None)
        if kwargs:
            raise TypeError('subscribe got an unexpected keyword argument %s' % (next(iter(kwargs)),))
        request = SubscribeRequest()
        for (service, min_distance, max_distance) in options:
            request.options.add(service=service, min_distance=min_distance, max_distance=max_distance)
        if max_retries is not None:
            request.max_retries = max_retries
        if disable_routes is not None:
            request.disable_routes = disable_routes
        sub = self.Subscription(self)
        self.wait_connected()
        response = self._make_simple_request(Subscribe, request, SubscribeResponse)
        sub._target_id = response.target_id
        self._subscriptions[sub] = request
        return sub

    def publish(self, name, handler, priority=0, distance=2, concurrency=1, queue_size=64):
        target_id = self._next_target_id
        self._next_target_id += 1
        self._handlers[target_id] = handler
        try:
            request = PublishRequest()
            request.name = name
            request.target_id = target_id
            request.settings.priority = priority
            request.settings.distance = distance
            request.settings.concurrency = concurrency
            request.settings.queue_size = queue_size
            pub = self.Publication(self, target_id)
            self.wait_connected()
            self._make_simple_request(Publish, request, PublishResponse)
        except:
            self._handlers.pop(target_id)
            raise
        self._publications[pub] = request
        return pub
