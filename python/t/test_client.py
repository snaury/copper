# -*- coding: utf-8 -*-
import pytest
from gevent import sleep
from copper.errors import ConnectionClosedError
from copper.client import CopperClient

def test_client(copper_node):
    with CopperClient(('unix', copper_node)) as client:
        def handler(stream):
            stream.write('Hello, world!')
        with client.publish('test:helloworld', handler):
            with client.subscribe('test:helloworld') as sub:
                assert sub.endpoints
                with sub.open() as stream:
                    assert stream.read(128) == 'Hello, world!'
                oldconn, client._conn = client._conn, None
                oldconn.close()
                with pytest.raises(ConnectionClosedError):
                    oldconn.wait()
                with sub.open() as stream:
                    assert stream.read(128) == 'Hello, world!'
