# -*- coding: utf-8 -*-
from gevent.event import Event

__all__ = [
    'Condition',
    'take_from_deque',
]

class Condition(object):
    def __init__(self):
        self._event = Event()

    def broadcast(self):
        try:
            self._event.set()
        finally:
            self._event.clear()

    def wait(self):
        self._event.wait()

def take_from_deque(d, size):
    """Takes up to size bytes from the deque"""
    if not d:
        return ''
    if len(d[0]) >= size:
        chunk = d.popleft()
        if len(chunk) > size:
            d.appendleft(chunk[size:])
            chunk = chunk[:size]
        return chunk
    elif len(d) == 1:
        return d.popleft()
    result = []
    remaining = size
    while d and remaining > 0:
        chunk = d.popleft()
        if len(chunk) > remaining:
            d.appendleft(chunk[remaining:])
            chunk = chunk[:remaining]
        remaining -= len(chunk)
        result.append(chunk)
    return ''.join(result)
