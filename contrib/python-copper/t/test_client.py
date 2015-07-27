# -*- coding: utf-8 -*-
import os
import time
import pytest
import gevent
from collections import defaultdict
from gevent import sleep
from gevent import Timeout
from gevent.event import Event
from copper.errors import ConnectionShutdownError
from copper.errors import NoRouteError

def test_client(copper_client):
    def handler(stream):
        stream.write('Hello, world!')
    with copper_client.publish('test:helloworld', handler):
        with copper_client.subscribe('test:helloworld') as sub:
            assert sub.endpoints
            with sub.open() as stream:
                assert stream.read() == 'Hello, world!'
            oldconn, copper_client._conn = copper_client._conn, None
            oldconn.close()
            with sub.open() as stream:
                assert stream.read() == 'Hello, world!'

def test_client_shutdown_empty(copper_client):
    def handler(stream):
        pass
    with copper_client.publish('test:helloworld', handler):
        pass
    copper_client.shutdown()

def test_client_shutdown(copper_client):
    have_stream = Event()
    may_respond = Event()
    def handler(stream):
        have_stream.set()
        may_respond.wait()
        stream.write('Hello, world!')
    with copper_client.publish('test:helloworld', handler):
        with copper_client.subscribe('test:helloworld') as sub:
            with sub.open() as stream:
                have_stream.wait()
                with pytest.raises(Timeout):
                    with Timeout(0.005):
                        # This initiates the shutdown, but it should not
                        # complete on its own, because handler is still
                        # running. Being stopped with a timeout does not
                        # stop the shutdown procedure.
                        copper_client.shutdown()
                # Verify our handler can still reply successfully
                may_respond.set()
                assert stream.read() == 'Hello, world!'
            with sub.open() as stream:
                # Verify any new streams fail with ECONNSHUTDOWN (since our
                # code didn't unpublish the service), and don't reach our
                # handler.
                with pytest.raises(ConnectionShutdownError):
                    stream.read()
            # Verify shutdown now finishes successfully.
            copper_client.shutdown()

def test_client_reconnect_active(copper_client):
    have_stream = Event()
    may_respond = Event()
    def handler(stream):
        have_stream.set()
        may_respond.wait()
        stream.write('Hello, world!')
    with copper_client.publish('test:helloworld', handler):
        with copper_client.subscribe('test:helloworld') as sub:
            with sub.open() as stream:
                have_stream.wait()
            # We close the old connection here, but note that it is still
            # stuck in the handler and will not get unstuck on its own.
            oldconn, copper_client._conn = copper_client._conn, None
            oldconn.close()
            # Here we verify, that despite old handler being active the
            # client actually reconnects successfully and we get a reply.
            have_stream.clear()
            with sub.open() as stream:
                have_stream.wait()
                may_respond.set()
                assert stream.read() == 'Hello, world!'

def assert_change_eq(change, target_id, name, priority, distance, concurrency, queue_size):
    assert change.target_id == target_id
    assert change.name == name
    assert change.settings.priority == priority
    assert change.settings.distance == distance
    assert change.settings.concurrency == concurrency
    assert change.settings.queue_size == queue_size

def test_service_changes(copper_client):
    def handler(stream):
        pass
    changes_stream = copper_client.service_changes()
    with copper_client.publish('test:helloworld', handler):
        changes = next(changes_stream)
        assert not changes.removed
        assert len(changes.changed) == 1
        assert_change_eq(changes.changed[0], 1, 'test:helloworld', 0, 2, 1, 64)
        with copper_client.publish('test:helloworld', handler):
            changes = next(changes_stream)
            assert not changes.removed
            assert len(changes.changed) == 1
            assert_change_eq(changes.changed[0], 1, 'test:helloworld', 0, 2, 2, 128)
        changes = next(changes_stream)
        assert not changes.removed
        assert len(changes.changed) == 1
        assert_change_eq(changes.changed[0], 1, 'test:helloworld', 0, 2, 1, 64)
        with copper_client.publish('test:helloworld', handler, priority=1):
            changes = next(changes_stream)
            assert not changes.removed
            assert len(changes.changed) == 1
            assert_change_eq(changes.changed[0], 2, 'test:helloworld', 1, 2, 1, 64)
        changes = next(changes_stream)
        assert changes.removed == [2]
        assert not changes.changed
    changes = next(changes_stream)
    assert changes is not None
    assert changes.removed == [1]
    assert not changes.changed

def assert_route_eq(route, options, weight):
    assert len(route.options) == len(options)
    for option, expected in zip(route.options, options):
        assert (option.service, option.min_distance, option.max_distance) == expected
    assert route.weight == weight

def test_routes(copper_client):
    copper_client.set_route('test:helloroute', 'test:helloworld1', 'test:helloworld2')
    assert copper_client.list_routes() == ['test:helloroute']
    routes = copper_client.lookup_route('test:helloroute')
    assert len(routes) == 2
    assert_route_eq(routes[0], [('test:helloworld1', 0, 1), ('test:helloworld1', 2, 2)], 1)
    assert_route_eq(routes[1], [('test:helloworld2', 0, 1), ('test:helloworld2', 2, 2)], 1)
    copper_client.set_route('test:helloroute')
    assert copper_client.list_routes() == []
    assert copper_client.lookup_route('test:helloroute') == []

def test_routed_services(copper_client):
    def handler1(stream):
        stream.write('Hello from handler1')
    def handler2(stream):
        stream.write('Hello from handler2')
    with copper_client.publish('test:hello1', handler1):
        with copper_client.publish('test:hello2', handler2):
            with copper_client.subscribe('test:hello2') as sub:
                # Verify that requests go normally.
                with sub.open() as stream:
                    assert stream.read() == 'Hello from handler2'
                # Re-route test:hello2 to point to test:hello1 and verify that
                # requests really go to a different service.
                copper_client.set_route('test:hello2', 'test:hello1')
                with sub.open() as stream:
                    assert stream.read() == 'Hello from handler1'
                # Re-route test:hello1 to some non-existant service and since
                # routes always use direct service names test:hello2 should not
                # be affected by this ne wroute.
                copper_client.set_route('test:hello1', 'test:whatever')
                with sub.open() as stream:
                    assert stream.read() == 'Hello from handler1'
                # Re-route test:hello2 to some non-existant service and verify
                # that requests don't go to the real service just because the
                # route has a non-existant destination.
                copper_client.set_route('test:hello2', 'test:whatever')
                with pytest.raises(NoRouteError):
                    with sub.open() as stream:
                        stream.read()
                # Add multiple routes for test:hello2 and verify that randomizer
                # does not consider the non-existant route as a possibility.
                copper_client.set_route('test:hello2', 'test:whatever', 'test:hello1')
                for _ in xrange(32):
                    with sub.open() as stream:
                        assert stream.read() == 'Hello from handler1'
                # Add a second option for test:hello2 and verify that it is used
                # when the first option cannot be reached.
                copper_client.set_route('test:hello2', ['test:whatever', 'test:hello1'])
                with sub.open() as stream:
                    assert stream.read() == 'Hello from handler1'
                # Drop the route and verify that requests go normally again.
                copper_client.set_route('test:hello2')
                with sub.open() as stream:
                    assert stream.read() == 'Hello from handler2'
                # Set multiple routes for test:hello2 and verify that randomizer
                # selects all of them at least once.
                counters = defaultdict(int)
                copper_client.set_route('test:hello2', 'test:hello1', 'test:hello2')
                for _ in xrange(32):
                    with sub.open() as stream:
                        counters[stream.read()] += 1
                assert sorted(counters) == [
                    'Hello from handler1',
                    'Hello from handler2',
                ]
                # Set a route that has a priority for the first service and
                # verify that requests don't go to the second service.
                copper_client.set_route('test:hello2', ['test:hello1', 'test:hello2'])
                for _ in xrange(32):
                    with sub.open() as stream:
                        assert stream.read() == 'Hello from handler1'

if os.environ.get('SPEEDTEST', '').strip().lower() in ('1', 'yes', 'true'):
    @pytest.mark.parametrize('concurrency', [1, 8, 64, 512])
    def test_speed(copper_client, concurrency):
        def handler(stream):
            x = stream.read(1)
            stream.write(x)
        with copper_client.publish('test:myservice', handler, concurrency=concurrency, queue_size=concurrency*2):
            with copper_client.subscribe('test:myservice') as sub:
                def make_request():
                    with sub.open() as stream:
                        stream.write('x')
                        stream.close_write()
                        assert stream.read(1) == 'x'
                for _ in xrange(32):
                    make_request()
                start_time = time.time()
                stop_time = start_time + 1.0
                timings = []
                def worker_func():
                    while True:
                        t0 = time.time()
                        make_request()
                        t1 = time.time()
                        timings.append(t1 - t0)
                        if t1 >= stop_time:
                            break
                workers = [gevent.spawn(worker_func) for _ in xrange(concurrency)]
                for worker in gevent.wait(workers):
                    worker.get()
                stop_time = time.time()
                if not timings:
                    print 'No timings have been collected'
                    return
                timings.sort()
                percentiles = (50, 90, 95, 96, 97, 98, 99, 100)
                values = []
                for p in percentiles:
                    index = ((len(timings) * p + 99) // 100) - 1
                    if index < 0:
                        index = 0
                    values.append(timings[index])
                print 'Concurrency %s: %s avg=%.3fms %.3freqs/s' % (
                    concurrency,
                    ' '.join(
                        'p%d=%.3fms' % (p, value * 1000.0)
                        for (p, value) in zip(percentiles, values)
                    ),
                    sum(timings) * 1000.0 / len(timings),
                    len(timings) / (stop_time - start_time),
                )
