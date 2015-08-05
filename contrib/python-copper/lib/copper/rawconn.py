# -*- coding: utf-8 -*-
import sys
import time
import logging
from gevent import spawn
from gevent import Timeout
from gevent.hub import Waiter
from gevent.hub import get_hub
from gevent.event import Event
from collections import deque
from socket import error as socket_error
from .frames import (
    Frame,
    PingFrame,
    DataFrame,
    ResetFrame,
    WindowFrame,
    SettingsFrame,
    FLAG_PING_ACK,
    FLAG_DATA_EOF,
    FLAG_DATA_OPEN,
    FLAG_DATA_ACK,
    FLAG_RESET_READ,
    FLAG_RESET_WRITE,
    FLAG_SETTINGS_ACK,
    FrameReader,
    FrameWriter,
)
from .errors import (
    NoTargetError,
    StreamClosedError,
    ConnectionClosedError,
    ConnectionTimeoutError,
    ConnectionShutdownError,
    InvalidFrameError,
    InvalidStreamError,
    WindowOverflowError,
)
from .util import (
    Condition,
    take_from_deque,
)

__all__ = [
    'RawConn',
]

INACTIVITY_TIMEOUT = 60
DEFAULT_STREAM_WINDOW = 65536
DEFAULT_STREAM_WRITE_BUFFER = 1024*1024

log = logging.getLogger(__name__)

class RawConn(object):
    def __init__(self, sock, handler=None, is_server=False):
        self._sock = sock
        self._handler = handler
        self._reader = FrameReader(sock)
        self._writer = FrameWriter(sock)
        self._closed = False
        self._shutdown = False
        self._failure = None
        self._failure_out = None
        self._close_ready = Event()
        self._write_ready = Event()
        self._ping_acks = []
        self._ping_reqs = []
        self._ping_waiters = {}
        self._ctrl_streams = set()
        self._data_streams = set()
        self._streams = {}
        self._is_server = is_server
        if self._is_server:
            self._next_stream_id = 2
        else:
            self._next_stream_id = 1
        self._local_stream_window = DEFAULT_STREAM_WINDOW
        self._remote_stream_window = DEFAULT_STREAM_WINDOW
        self._active_workers = 0
        self._active_handlers = 0
        self._workers_finished = Event()
        self._workers_finished.set()
        self._handlers_finished = Event()
        self._handlers_finished.set()
        self._spawn(self._read_loop)
        self._spawn(self._write_loop)

    def __enter__(self):
        return self

    def __exit__(self, exc_type=None, exc_val=None, exc_tb=None):
        self.close()
        self._workers_finished.wait()

    def _spawn(self, *args, **kwargs):
        g = spawn(*args, **kwargs)
        g.rawlink(self._worker_finished)
        self._active_workers += 1
        self._workers_finished.clear()
        return g

    def _spawn_handler(self, *args, **kwargs):
        g = self._spawn(*args, **kwargs)
        g.rawlink(self._handler_finished)
        self._active_handlers += 1
        self._handlers_finished.clear()
        return g

    def _worker_finished(self, g):
        self._active_workers -= 1
        if self._active_workers == 0:
            self._workers_finished.set()

    def _handler_finished(self, g):
        self._active_handlers -= 1
        if self._active_handlers == 0:
            self._handlers_finished.set()

    def _close_with_error(self, error):
        if not self._closed:
            self._closed = True
            self._failure = error
            if self._failure_out is None:
                if isinstance(error, ConnectionClosedError):
                    self._failure_out = ConnectionShutdownError()
                else:
                    self._failure_out = error
            self._close_ready.set()
            self._write_ready.set()
            for stream in self._streams.values():
                stream._close_with_error(error)

    @property
    def error(self):
        if self._closed:
            return self._failure
        return None

    def close(self):
        self._close_with_error(ConnectionClosedError())

    def shutdown(self):
        self._shutdown = True
        self._handlers_finished.wait()

    def wait_done(self):
        self._workers_finished.wait()

    def wait_closed(self):
        while not self._closed:
            self._close_ready.wait()

    def ping(self, value):
        if self._closed:
            raise self._failure
        waiter = Waiter()
        waiters = self._ping_waiters.get(value)
        if waiters is not None:
            waiters.append(waiter)
        else:
            self._ping_waiters[value] = [waiter]
        self._ping_reqs.append(value)
        self._write_ready.set()
        return waiter.get()

    def new_stream(self):
        if self._closed:
            raise self._failure
        while True:
            stream_id = self._next_stream_id
            self._next_stream_id += 2
            if self._next_stream_id >= 0x80000000:
                self._next_stream_id -= 0x80000000
                if not self._next_stream_id:
                    self._next_stream_id = 2
            if stream_id not in self._streams:
                return RawStream.new_outgoing(self, stream_id)

    def _handle_stream(self, stream):
        if self._handler is None:
            stream.close_with_error(NoTargetError())
            return
        try:
            self._handler(stream)
        except:
            log.debug('Stream handler failed', exc_info=True)
            stream.close_with_error(sys.exc_info()[1])
        else:
            stream.close()

    def _remove_stream(self, stream):
        stream_id = stream._stream_id
        if self._streams.get(stream_id) is stream:
            del self._streams[stream_id]
            self._ctrl_streams.discard(stream)
            self._data_streams.discard(stream)

    def _process_ping_frame(self, frame):
        if frame.flags & FLAG_PING_ACK:
            waiters = self._ping_waiters.get(frame.value)
            if len(waiters) > 1:
                waiter = waiters.pop(0)
            elif waiters:
                del self._ping_waiters[frame.value]
                waiter = waiters[0]
            else:
                waiter = None
            if waiter is not None:
                get_hub().loop.run_callback(waiter.switch)
        else:
            self._ping_acks.append(frame.value)
            self._write_ready.set()

    def _process_data_frame(self, frame):
        if frame.stream_id == 0:
            if frame.flags != 0 or len(frame.data) != 0:
                raise InvalidStreamError("stream 0 cannot be used for data")
            return
        stream = self._streams.get(frame.stream_id)
        if frame.flags & FLAG_DATA_OPEN:
            if (frame.stream_id & 1) == (0 if self._is_server else 1):
                raise InvalidStreamError('stream 0x%08x cannot be used for opening streams' % (frame.stream_id,))
            if stream is not None:
                raise InvalidStreamError('stream 0x%08x cannot be reopened until fully closed' % (frame.stream_id,))
            stream = RawStream.new_incoming(self, frame.stream_id)
            if self._closed:
                # We are closed and ignore valid DATA frames
                stream.close_with_error(self._failure)
                return
            if self._shutdown:
                # We are shutting down and close new streams
                stream.close_with_error(ConnectionShutdownError())
                return
            self._spawn_handler(self._handle_stream, stream)
        else:
            if stream is None:
                raise InvalidStreamError("stream 0x%08x cannot be found" % (frame.stream_id,))
            if self._closed:
                # We are closed and ignore valid DATA frames
                return
        stream._process_data_frame(frame)

    def _process_reset_frame(self, frame):
        if frame.stream_id == 0:
            if self._failure_out is None:
                self._failure_out = ConnectionShutdownError()
            raise frame.error
        stream = self._streams.get(frame.stream_id)
        if stream is None:
            # It's ok to receive RESET for a dead stream
            return
        if self._closed:
            # We are closed and ignore valid RESET frames
            return
        stream._process_reset_frame(frame)

    def _process_window_frame(self, frame):
        stream = self._streams.get(frame.stream_id)
        if stream is None:
            # It's ok to receive WINDOW for a dead stream
            return
        stream._process_window_frame(frame)

    def _process_settings_frame(self, frame):
        raise InvalidFrameError("settings frames are not supported yet")

    _process_frame_by_type = {
        PingFrame: _process_ping_frame,
        DataFrame: _process_data_frame,
        ResetFrame: _process_reset_frame,
        WindowFrame: _process_window_frame,
        SettingsFrame: _process_settings_frame,
    }

    def _read_loop(self):
        while True:
            try:
                frame = None
                with Timeout(INACTIVITY_TIMEOUT, False):
                    frame = self._reader.read_frame()
                if frame is None:
                    raise ConnectionTimeoutError()
                process = self._process_frame_by_type.get(frame.__class__)
                if process is None:
                    raise InvalidFrameError('received an unsupported frame')
                process(self, frame)
            except:
                e = sys.exc_info()[1]
                if not isinstance(e, socket_error):
                    log.error('Connection readloop failed', exc_info=True)
                self._close_with_error(e)
                break
        # Nothing else will be read, clean up
        self._ping_reqs = []
        while self._ping_waiters:
            _, waiters = self._ping_waiters.popitem()
            for waiter in waiters:
                get_hub().loop.run_callback(waiter.throw, self._failure)
        # TODO: settings stuff
        while self._streams:
            _, stream = self._streams.popitem()
            stream._close_with_error(self._failure)

    def _add_ctrl_stream(self, stream):
        self._ctrl_streams.add(stream)
        self._write_ready.set()

    def _add_data_stream(self, stream):
        self._data_streams.add(stream)
        self._write_ready.set()

    def _write_frames(self):
        while True:
            self._write_ready.clear()
            send_detected = False
            if self._failure_out is not None:
                self._writer.write_frame(ResetFrame(0, 0, self._failure_out))
                self._writer.flush()
                return False
            if self._ping_acks:
                ping_acks, self._ping_acks = self._ping_acks, []
                for value in ping_acks:
                    self._writer.write_frame(PingFrame(FLAG_PING_ACK, value))
                continue
            if self._ping_reqs:
                ping_reqs, self._ping_reqs = self._ping_reqs, []
                for value in ping_reqs:
                    self._writer.write_frame(PingFrame(0, value))
                continue
            # TODO: settings frames
            if self._ctrl_streams:
                stream = self._ctrl_streams.pop()
                stream._send_ctrl(self._writer)
                continue
            if self._data_streams:
                stream = self._data_streams.pop()
                stream._send_data(self._writer)
                continue
            if self._write_ready.is_set():
                # signal channel active, must try again
                continue
            # we are done sending stuff
            break
        return True

    @property
    def _next_mandatory_send_time(self):
        return self._writer.last_send_time + INACTIVITY_TIMEOUT * 2 // 3

    def _write_loop(self):
        while True:
            delay = self._next_mandatory_send_time - time.time()
            if delay > 0:
                with Timeout(delay, False):
                    self._write_ready.wait()
            try:
                if not self._write_frames():
                    break
                if not self._writer.buffer and self._next_mandatory_send_time <= time.time():
                    # we must send an empty data frame, however we haven't sent anything yet
                    self._writer.write_frame(DataFrame(0, 0, ''))
                self._writer.flush()
            except:
                e = sys.exc_info()[1]
                if not isinstance(e, socket_error):
                    log.error('Connection writeloop failed', exc_info=True)
                self._close_with_error(e)
                break
        self._sock.close()

class RawStream(object):
    def __init__(self, conn, stream_id):
        self._conn = conn
        self._stream_id = stream_id
        self._is_outgoing = False
        self._error = None
        self._readbuf = deque()
        self._readsize = 0
        self._readleft = conn._local_stream_window
        self._readincrement = 0
        self._read_error = None
        self._writebuf = deque()
        self._writesize = 0
        self._writeleft = conn._remote_stream_window
        self._write_error = None
        self._seen_eof = False
        self._sent_eof = False
        self._seen_ack = False
        self._seen_reset = False
        self._need_open = False
        self._need_ack = False
        self._need_reset = False
        self._need_eof = False
        self._read_cond = Condition()
        self._write_cond = Condition()
        self._flush_cond = Condition()
        self._read_closed_event = Event()
        self._write_closed_event = Event()
        self._acknowledged_event = Event()
        self.max_write_buffer_size = DEFAULT_STREAM_WRITE_BUFFER
        conn._streams[stream_id] = self

    @classmethod
    def new_incoming(cls, conn, stream_id):
        return cls(conn, stream_id)

    @classmethod
    def new_outgoing(cls, conn, stream_id):
        self = cls(conn, stream_id)
        self._is_outgoing = True
        self._need_open = True
        self._schedule_ctrl()
        return self

    def __enter__(self):
        return self

    def __exit__(self, exc_type=None, exc_val=None, exc_tb=None):
        self.close()

    def _cleanup(self):
        if self._seen_eof and self._sent_eof:
            self._conn._remove_stream(self)

    def _schedule_ctrl(self):
        self._conn._add_ctrl_stream(self)

    def _schedule_data(self):
        self._conn._add_data_stream(self)

    def _clear_write_buffer(self):
        self._writebuf.clear()
        self._writesize = 0
        self._flush_cond.broadcast()
        if self._need_eof:
            self._schedule_ctrl()

    def _prepare_data(self, maxsize=0xffffff):
        n = self._writesize
        if n > self._writeleft:
            n = self._writeleft
        if n > maxsize:
            n = maxsize
        if n > 0:
            data = take_from_deque(self._writebuf, n)
            assert len(data) == n
            self._writesize -= n
            self._writeleft -= n
            if self.max_write_buffer_size is not None and self._writesize < self.max_write_buffer_size:
                self._write_cond.broadcast()
            return data
        return ''

    def _outgoing_flags(self):
        flags = 0
        if self._need_open:
            self._need_open = False
            flags |= FLAG_DATA_OPEN
        if self._need_ack:
            self._need_ack = False
            flags |= FLAG_DATA_ACK
        if self._active_eof():
            self._need_eof = False
            self._sent_eof = True
            self._cleanup()
            flags |= FLAG_DATA_EOF
        return flags

    def _active_eof(self):
        if self._need_eof and not self._writebuf:
            return not self._need_reset and isinstance(self._write_error, StreamClosedError)
        return False

    def _active_data(self):
        return self._writeleft > 0 and self._writebuf

    def _send_ctrl(self, writer):
        if not self._writebuf and self._need_open and self._need_reset and self._need_eof and not self._need_ack:
            self._seen_eof = True
            self._sent_eof = True
            self._need_open = False
            self._need_reset = False
            self._need_eof = False
            self._cleanup()
            return
        while True:
            if (self._need_open or self._need_ack) and not self._sent_eof:
                data = self._prepare_data()
                flags = self._outgoing_flags()
                writer.write_frame(DataFrame(
                    self._stream_id,
                    flags,
                    data,
                ))
                if not self._writebuf:
                    self._flush_cond.broadcast()
                continue
            if self._readincrement > 0:
                increment = self._readincrement
                self._readleft += increment
                self._readincrement = 0
                writer.write_frame(WindowFrame(
                    self._stream_id,
                    0,
                    increment,
                ))
                continue
            if self._need_reset:
                self._need_reset = False
                flags = FLAG_RESET_READ
                error = self._read_error
                if isinstance(error, EOFError):
                    error = StreamClosedError()
                if self._need_eof and not self._writebuf and error == self._write_error:
                    self._need_eof = False
                    self._sent_eof = True
                    self._cleanup()
                    flags |= FLAG_RESET_WRITE
                if isinstance(error, ConnectionClosedError):
                    error = ConnectionShutdownError()
                writer.write_frame(ResetFrame(
                    self._stream_id,
                    flags,
                    error,
                ))
                continue
            if self._need_eof and not self._writebuf:
                self._need_eof = False
                self._sent_eof = True
                self._cleanup()
                if not isinstance(self._write_error, StreamClosedError):
                    flags = FLAG_RESET_WRITE
                    error = self._write_error
                    if isinstance(error, ConnectionClosedError):
                        error = ConnectionShutdownError()
                    writer.write_frame(ResetFrame(
                        self._stream_id,
                        flags,
                        error,
                    ))
                else:
                    writer.write_frame(DataFrame(
                        self._stream_id,
                        FLAG_DATA_EOF,
                        '',
                    ))
                continue
            break

    def _send_data(self, writer):
        data = self._prepare_data()
        if data:
            flags = self._outgoing_flags()
            if self._active_data():
                self._schedule_data()
            elif self._need_eof and not self._writebuf:
                self._schedule_ctrl()
            writer.write_frame(DataFrame(
                self._stream_id,
                flags,
                data,
            ))
            if not self._writebuf:
                self._flush_cond.broadcast()

    def _process_data_frame(self, frame):
        if self._seen_eof:
            raise InvalidStreamError('stream 0x%08x cannot have DATA after EOF' % (self._stream_id))
        if frame.flags & FLAG_DATA_ACK:
            self._seen_ack = True
            self._acknowledged_event.set()
        if frame.data:
            if len(frame.data) > self._readleft:
                raise WindowOverflowError('stream 0x%08x received %d+%d bytes, which is more than %d bytes window' % (self._stream_id, self._readsize, len(frame.data), self._readleft))
            if self._read_error is None:
                self._readbuf.append(frame.data)
                self._readsize += len(frame.data)
                self._read_cond.broadcast()
            self._readleft -= len(frame.data)
        if frame.flags & FLAG_DATA_EOF:
            self._seen_eof = True
            self._set_read_error(EOFError())
            self._cleanup()

    def _process_reset_frame(self, frame):
        if frame.flags & FLAG_RESET_READ:
            self._seen_reset = True
            self._clear_write_buffer()
            self._set_write_error(frame.error)
        if frame.flags & FLAG_RESET_WRITE:
            error = frame.error
            if isinstance(error, StreamClosedError):
                error = EOFError()
            self._seen_eof = True
            self._need_reset = False
            self._set_read_error(error)
            self._cleanup()

    def _process_window_frame(self, frame):
        if frame.increment <= 0:
            raise InvalidFrameError('stream 0x%08x received invalid increment %d' % (self._stream_id, frame.increment))
        if self._writeleft <= 0 and self._writebuf:
            self._writeleft += frame.increment
            if self._writeleft > 0:
                self._schedule_data()
        else:
            self._writeleft += frame.increment

    def _set_read_error(self, error):
        if self._read_error is None:
            self._read_error = error
            self._read_closed_event.set()
            self._acknowledged_event.set()
            if not self._seen_eof:
                self._need_reset = True
                self._schedule_ctrl()
            self._readincrement = 0
            self._read_cond.broadcast()

    def _set_write_error(self, error):
        if self._write_error is None:
            self._write_error = error
            self._write_closed_event.set()
            self._need_eof = True
            if not self._writebuf:
                # We had no pending data, but now we need to send EOF/RESET,
                # which can only be done in a ctrl phase, so make sure to
                # schedule it.
                self._schedule_ctrl()
            self._write_cond.broadcast()
            self._flush_cond.broadcast()

    def _close_with_error(self, error):
        if error is None or isinstance(error, EOFError):
            error = StreamClosedError()
        if self._error is None:
            self._error = error
            self._set_read_error(error)
            self._set_write_error(error)

    @property
    def closed(self):
        return self._error is not None

    def close(self):
        self._close_with_error(StreamClosedError())

    def close_with_error(self, error):
        self._close_with_error(error)

    def close_read(self):
        self._set_read_error(StreamClosedError())

    def close_read_error(self, error):
        if error is None or isinstance(error, EOFError):
            error = StreamClosedError()
        self._set_read_error(error)

    def close_write(self):
        self._set_write_error(StreamClosedError())

    def close_write_error(self, error):
        if error is None or isinstance(error, EOFError):
            error = StreamClosedError()
        self._set_write_error(error)

    def peek(self):
        while not self._readbuf:
            if self._read_error is not None:
                if isinstance(self._read_error, EOFError):
                    return ''
                raise self._read_error
            self._read_cond.wait()
        if len(self._readbuf) > 1:
            result = ''.join(self._readbuf)
            self._readbuf.clear()
            self._readbuf.append(result)
        return self._readbuf[0]

    def discard(self, n):
        if n <= 0:
            return 0
        elif self._readsize <= n:
            n = self._readsize
            self._readbuf.clear()
            self._readsize = 0
        else:
            remaining = n
            while self._readbuf and remaining > 0:
                chunk = self._readbuf.popleft()
                if len(chunk) > remaining:
                    self._readbuf.appendleft(chunk[remaining:])
                    chunk = chunk[:remaining]
                self._readsize -= len(chunk)
                remaining -= len(chunk)
            n -= remaining
        if self._read_error is None:
            self._readincrement += n
            self._schedule_ctrl()
        return n

    def recv(self, bufsize):
        if bufsize <= 0:
            # Don't block if trying to receive 0 bytes
            if self._read_error is not None and not isinstance(self._read_error, EOFError):
                raise self._read_error
            return ''
        while not self._readbuf:
            if self._read_error is not None:
                if isinstance(self._read_error, EOFError):
                    return ''
                raise self._read_error
            self._read_cond.wait()
        data = take_from_deque(self._readbuf, bufsize)
        self._readsize -= len(data)
        if self._read_error is None:
            self._readincrement += len(data)
            self._schedule_ctrl()
        return data

    def read(self, n=-1):
        result = []
        while n != 0:
            while not self._readbuf:
                if self._read_error is not None:
                    if isinstance(self._read_error, EOFError):
                        return ''.join(result)
                    raise self._read_error
                self._read_cond.wait()
            chunk = self._readbuf.popleft()
            if n > 0 and len(chunk) > n:
                self._readbuf.appendleft(chunk[n:])
                chunk = chunk[:n]
            self._readsize -= len(chunk)
            if self._read_error is None:
                self._readincrement += len(chunk)
                self._schedule_ctrl()
            if n > 0:
                n -= len(chunk)
            result.append(chunk)
        return ''.join(result)

    def readline(self, n=-1):
        result = []
        while n != 0:
            while not self._readbuf:
                if self._read_error is not None:
                    if isinstance(self._read_error, EOFError):
                        return ''.join(result)
                    raise self._read_error
                self._read_cond.wait()
            chunk = self._readbuf.popleft()
            size = chunk.find('\n') + 1
            if size > 0:
                stop = True
            else:
                size = len(chunk)
                stop = False
            if n > 0 and size >= n:
                size = n
                stop = True
            if len(chunk) > size:
                self._readbuf.appendleft(chunk[size:])
                chunk = chunk[:size]
            self._readsize -= len(chunk)
            if self._read_error is None:
                self._readincrement += size
                self._schedule_ctrl()
            result.append(chunk)
            if stop:
                break
            if n > 0:
                n -= size
        return ''.join(result)

    def send(self, data):
        if not isinstance(data, (bytes, memoryview)):
            data = memoryview(data)
        while self._write_error is None and self.max_write_buffer_size is not None and self._writesize >= self.max_write_buffer_size:
            self._write_cond.wait()
        if self._write_error is not None:
            raise self._write_error
        if self.max_write_buffer_size is not None:
            data = data[:self.max_write_buffer_size-self._writesize]
        if isinstance(data, memoryview):
            data = data.tobytes()
        self._writebuf.append(data)
        self._writesize += len(data)
        if self._writeleft > 0:
            self._schedule_data()
        return len(data)

    def sendall(self, data):
        if not isinstance(data, memoryview):
            data = memoryview(data)
        while data:
            n = self.send(data)
            data = data[n:]

    write = sendall

    def getpeername(self):
        return self._conn._sock.getpeername()

    def getsockname(self):
        return self._conn._sock.getsockname()

    def flush(self):
        while self._write_error is None and self._writebuf:
            self._flush_cond.wait()
        if self._write_error is not None:
            raise self._write_error

    @property
    def read_error(self):
        return self._read_error

    def wait_read_closed(self):
        self._read_closed_event.wait()
        return self._read_error

    @property
    def write_error(self):
        return self._write_error

    def wait_write_closed(self):
        self._write_closed_event.wait()
        return self._write_error

    def acknowledge(self):
        if self._write_error is not None:
            raise self._write_error
        self._need_ack = True
        self._schedule_ctrl()

    @property
    def acknowledged(self):
        return self._seen_ack

    def wait_acknowledged(self):
        self._acknowledged_event.wait()
        return self._seen_ack

    @property
    def stream_id(self):
        return self._stream_id
