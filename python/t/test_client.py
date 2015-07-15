# -*- coding: utf-8 -*-
import pytest
from gevent import sleep
from gevent import Timeout
from gevent.event import Event
from copper.errors import ConnectionClosedError
from copper.errors import ConnectionShutdownError
from copper.client import CopperClient

def test_client(copper_node):
    with CopperClient(('unix', copper_node)) as client:
        def handler(stream):
            stream.write('Hello, world!')
        with client.publish('test:helloworld', handler):
            with client.subscribe('test:helloworld') as sub:
                assert sub.endpoints
                with sub.open() as stream:
                    assert stream.read() == 'Hello, world!'
                oldconn, client._conn = client._conn, None
                oldconn.close()
                with sub.open() as stream:
                    assert stream.read() == 'Hello, world!'

def test_client_shutdown(copper_node):
    with CopperClient(('unix', copper_node)) as client:
        have_stream = Event()
        may_respond = Event()
        def handler(stream):
            have_stream.set()
            may_respond.wait()
            stream.write('Hello, world!')
        with client.publish('test:helloworld', handler):
            with client.subscribe('test:helloworld') as sub:
                with sub.open() as stream:
                    have_stream.wait()
                    with pytest.raises(Timeout):
                        with Timeout(0.005):
                            # This initiates the shutdown, but it should not
                            # complete on its own, because handler is still
                            # running. Being stopped with a timeout does not
                            # stop the shutdown procedure.
                            client.shutdown()
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
                client.shutdown()

def test_client_reconnect_active(copper_node):
    with CopperClient(('unix', copper_node)) as client:
        have_stream = Event()
        may_respond = Event()
        def handler(stream):
            have_stream.set()
            may_respond.wait()
            stream.write('Hello, world!')
        with client.publish('test:helloworld', handler):
            with client.subscribe('test:helloworld') as sub:
                with sub.open() as stream:
                    have_stream.wait()
                # We close the old connection here, but note that it is still
                # stuck in the handler and will not get unstuck on its own.
                oldconn, client._conn = client._conn, None
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

def test_service_changes(copper_node):
    with CopperClient(('unix', copper_node)) as client:
        def handler(stream):
            pass
        changes_stream = client.service_changes()
        with client.publish('test:helloworld', handler):
            changes = next(changes_stream)
            assert not changes.removed
            assert len(changes.changed) == 1
            assert_change_eq(changes.changed[0], 1, 'test:helloworld', 0, 2, 1, 64)
            with client.publish('test:helloworld', handler):
                changes = next(changes_stream)
                assert not changes.removed
                assert len(changes.changed) == 1
                assert_change_eq(changes.changed[0], 1, 'test:helloworld', 0, 2, 2, 128)
            changes = next(changes_stream)
            assert not changes.removed
            assert len(changes.changed) == 1
            assert_change_eq(changes.changed[0], 1, 'test:helloworld', 0, 2, 1, 64)
            with client.publish('test:helloworld', handler, priority=1):
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
