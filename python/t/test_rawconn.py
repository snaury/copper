import pytest
from gevent import spawn
from gevent.socket import socketpair
from copper.rawconn import RawConn
from copper.errors import NoRouteError

def test_rawconn():
    a, b = socketpair()
    def server_handler(stream):
        data = stream.read_some(16)
        stream.write(data)
    server = RawConn(a, server_handler, True)
    client = RawConn(b)
    with client.open(0) as c:
        c.write('foobar')
        data = c.read_some(16)
        assert data == 'foobar'

def test_rawconn_error():
    a, b = socketpair()
    def server_handler(stream):
        stream.close_with_error(NoRouteError())
    server = RawConn(a, server_handler, True)
    client = RawConn(b)
    with client.open(0) as c:
        with pytest.raises(NoRouteError):
            data = c.peek()
