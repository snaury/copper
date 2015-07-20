# -*- coding: utf-8 -*-
import sys
import functools
from gevent.hub import get_hub
from gevent.pywsgi import WSGIHandler

__all__ = [
    'wsgi',
]

class FakeServer(object):
    """This is a fake WSGIServer for WSGIHandler"""

    base_env = {
        'GATEWAY_INTERFACE': 'CGI/1.1',
        'SERVER_SOFTWARE': 'copper.wsgi/0.0',
        'SCRIPT_NAME': '',
        'wsgi.version': (1, 0),
        'wsgi.run_once': False,
        'wsgi.multithread': False,
        'wsgi.multiprocess': False,
    }

    def __init__(self, application, log=None):
        self.application = application
        self.loop = get_hub().loop
        self.log = log

    def get_environ(self):
        environ = self.base_env.copy()
        environ['wsgi.url_scheme'] = 'http'
        environ['wsgi.errors'] = sys.stderr
        return environ

class FakeFile(object):
    """This is a fake file for WSGIHandler"""

    def __init__(self, stream, mode, bufsize):
        self._stream = stream
        self.read = stream.read
        self.readline = stream.readline
        self.write = stream.write
        self.flush = stream.flush
        self.close = stream.close
        self.name = '<copper stream>'
        self.mode = mode
        self.encoding = None

    @property
    def closed(self):
        return self._stream.closed

class FakeSocket(object):
    """This is a fake socket for WSGIHandler"""

    def __init__(self, stream):
        self._sock = stream
        self.recv = stream.recv
        self.send = stream.send
        self.sendall = stream.sendall
        self.close = stream.close
        self.getpeername = stream.getpeername
        self.getsockname = stream.getsockname

    def makefile(self, mode=None, bufsize=None):
        return FakeFile(self._sock, mode, bufsize)

def wsgi(application=None):
    """This decorator transforms a wsgi application into a copper handler"""
    def decorator(application):
        @functools.wraps(application)
        def handler(stream):
            socket = FakeSocket(stream)
            address = socket.getpeername()
            handler = WSGIHandler(socket, address, FakeServer(application))
            handler.handle()
        return handler
    if application is not None:
        return decorator(application)
    return decorator
