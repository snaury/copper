# -*- coding: utf-8 -*-
from gevent.socket import socket

__all__ = [
    'RawConn',
]

class _RawConnReader(object):
    def __init__(self, sock, bufsize=4096):
        self.sock = sock
        self.buffer = ''
        self.bufsize = bufsize

    def read(self, n):
        while len(self.buffer) < n:
            chunk = self.sock.recv(self.bufsize)
            if not chunk:
                raise EOFError()
            self.buffer += chunk
        if len(self.buffer) > n:
            result, self.buffer = self.buffer[:n], self.buffer[n:]
        else:
            result, self.buffer = self.buffer, ''
        return result

class _RawConnWriter(object):
    def __init__(self, sock, bufsize=4096):
        self.sock = sock
        self.buffer = ''
        self.bufsize = bufsize

    def write(self, data):
        self.buffer += data
        while len(self.buffer) >= self.bufsize:
            n = self.sock.send(self.buffer)
            self.buffer = self.buffer[n:]

    def flush(self):
        while self.buffer:
            n = self.sock.send(self.buffer)
            self.buffer = self.buffer[n:]

class RawConn(object):
    def __init__(self, sock):
        self.sock = sock
