# -*- coding: utf-8 -*-
import sys
import time
import traceback
from gevent import spawn
from gevent import Timeout
from gevent.hub import Waiter
from gevent.hub import get_hub
from gevent.event import Event
from gevent.socket import EBADF, error as socket_error
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

INACTIVITY_TIMEOUT = 60
DEFAULT_CONN_WINDOW = 1<<20
DEFAULT_STREAM_WINDOW = 65536

__all__ = [
    'RawConn',
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

class _RawConnReader(object):
    def __init__(self, sock, bufsize=4096):
        self.sock = sock
        self.buffer = ''
        self.bufsize = bufsize

    def peek(self):
        if not self.buffer:
            chunk = self.sock.recv(self.bufsize)
            if not chunk:
                return ''
            self.buffer = chunk
        return self.buffer

    def read(self, n):
        while len(self.buffer) < n:
            chunk = self.sock.recv(self.bufsize)
            if not chunk:
                raise EOFError('read(%r) stopped short after %d bytes' % (n, len(self.buffer)))
            self.buffer += chunk
        if len(self.buffer) > n:
            result, self.buffer = self.buffer[:n], self.buffer[n:]
        else:
            result, self.buffer = self.buffer, ''
        return result

    def read_frame(self):
        frame = Frame.load(self)
        #print 'READ(%s): %r' % (self.sock.getsockname(), frame)
        return frame

class _RawConnWriter(object):
    def __init__(self, sock, bufsize=4096):
        self.sock = sock
        self.buffer = ''
        self.bufsize = bufsize
        self.sent_marker = None

    def write(self, data):
        self.buffer += data
        while len(self.buffer) >= self.bufsize:
            self.sent_marker = object()
            n = self.sock.send(self.buffer)
            self.buffer = self.buffer[n:]

    def flush(self):
        while self.buffer:
            self.sent_marker = object()
            n = self.sock.send(self.buffer)
            self.buffer = self.buffer[n:]

    def write_frame(self, frame):
        #print 'WRITE(%s): %r' % (self.sock.getsockname(), frame)
        frame.dump(self)

class RawConn(object):
    DEBUG_HANDLERS = False

    def __init__(self, sock, handler=None, is_server=False):
        self._sock = sock
        self._handler = handler
        self._reader = _RawConnReader(sock)
        self._writer = _RawConnWriter(sock)
        self._closed = False
        self._shutdown = False
        self._failure = None
        self._failure_out = None
        self._close_ready = Event()
        self._write_ready = Event()
        self._ping_acks = []
        self._ping_reqs = []
        self._ping_waiters = {}
        self._window_acks = {}
        self._ctrl_streams = set()
        self._data_streams = set()
        self._streams = {}
        self._is_server = is_server
        if self._is_server:
            self._next_stream_id = 2
        else:
            self._next_stream_id = 1
        self._local_conn_window = DEFAULT_CONN_WINDOW
        self._local_stream_window = DEFAULT_STREAM_WINDOW
        self._remote_conn_window = DEFAULT_CONN_WINDOW
        self._remote_stream_window = DEFAULT_STREAM_WINDOW
        self._writeleft = self._remote_conn_window
        self._active_workers = 0
        self._active_handlers = 0
        self._workers_finished = Event()
        self._handlers_finished = Event()
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

    def _close_with_error(self, error, closed):
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
            # Don't send any pings that haven't been sent already
            self._ping_reqs = []
        for stream in self._streams.values():
            stream._close_with_error(error, closed)

    @property
    def error(self):
        if self._closed:
            return self._failure
        return None

    def close(self):
        self._close_with_error(ConnectionClosedError(), True)

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
            if self.DEBUG_HANDLERS:
                traceback.print_exc()
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
            if len(frame.data) > self._local_conn_window:
                raise WindowOverflowError('stream 0x%08x opened with %d bytes, which is more than %d bytes window' % (frame.stream_id, len(frame.data), self._local_conn_window))
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
        if len(frame.data) > 0:
            self._add_window_ack(0, len(frame.data))

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
        if frame.stream_id == 0:
            self._writeleft += frame.increment
            self._write_ready.set()
            return
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
                    import traceback
                    traceback.print_exc()
                self._close_with_error(e, False)
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
            stream._close_with_error(self._failure, False)

    def _stream_can_receive(self, stream_id):
        if stream_id == 0:
            return True
        stream = self._streams.get(stream_id)
        if stream is not None and stream._can_receive():
            return True
        return False

    def _add_window_ack(self, stream_id, increment):
        self._window_acks[stream_id] = self._window_acks.get(stream_id, 0) + increment
        self._write_ready.set()

    def _add_ctrl_stream(self, stream):
        self._ctrl_streams.add(stream)
        self._write_ready.set()

    def _add_data_stream(self, stream):
        self._data_streams.add(stream)
        self._write_ready.set()

    def _write_frames(self):
        while True:
            self._write_ready.clear()
            sent_marker = self._writer.sent_marker
            send_detected = False
            if self._ping_acks:
                ping_acks, self._ping_acks = self._ping_acks, []
                for value in ping_acks:
                    self._writer.write_frame(PingFrame(FLAG_PING_ACK, value))
                if sent_marker is not self._writer.sent_marker:
                    continue
            if self._ping_reqs:
                ping_reqs, self._ping_reqs = self._ping_reqs, []
                for value in ping_reqs:
                    self._writer.write_frame(PingFrame(0, value))
                if sent_marker is not self._writer.sent_marker:
                    continue
            # TODO: settings frames
            while self._window_acks:
                stream_id, increment = self._window_acks.popitem()
                self._writer.write_frame(WindowFrame(stream_id, 0, increment))
                if sent_marker is not self._writer.sent_marker:
                    send_detected = True
                    break
            if send_detected:
                continue
            if self._failure_out is not None:
                self._writer.write_frame(ResetFrame(0, 0, self._failure_out))
                self._writer.flush()
                return False
            while self._ctrl_streams:
                stream = self._ctrl_streams.pop()
                stream._send_ctrl(self._writer)
                if sent_marker is not self._writer.sent_marker:
                    send_detected = True
                    break
            if send_detected:
                continue
            while self._data_streams:
                stream = self._data_streams.pop()
                stream._send_data(self._writer)
                if sent_marker is not self._writer.sent_marker:
                    send_detected = True
                    break
            if send_detected:
                continue
            if self._write_ready.is_set():
                # signal channel active, must try again
                continue
            # we are done sending stuff
            break
        return True

    def _write_loop(self):
        nextdata = time.time() + INACTIVITY_TIMEOUT * 2 // 3
        while True:
            datarequired = True
            delay = nextdata - time.time()
            if delay > 0:
                with Timeout(delay, False):
                    self._write_ready.wait()
                    datarequired = False
            try:
                initial_sent_marker = self._writer.sent_marker
                if not self._write_frames():
                    break
                if datarequired and initial_sent_marker is self._writer.sent_marker and not self._writer.buffer:
                    # we must send an empty data frame, however we haven't sent anything yet
                    self._writer.write_frame(DataFrame(0, 0, ''))
                self._writer.flush()
                if initial_sent_marker is not self._writer.sent_marker:
                    # we have sent some data, restart the timer
                    nextdata = time.time() + INACTIVITY_TIMEOUT * 2 // 3
            except:
                e = sys.exc_info()[1]
                if not isinstance(e, socket_error):
                    import traceback
                    traceback.print_exc()
                self._close_with_error(e, False)
                break
        self._sock.close()

class RawStream(object):
    def __init__(self, conn, stream_id):
        self._conn = conn
        self._stream_id = stream_id
        self._is_outgoing = False
        self._reset_error = None
        self._readbuf = ''
        self._readleft = conn._local_stream_window
        self._read_error = None
        self._writebuf = ''
        self._writeleft = conn._remote_stream_window
        self._write_error = None
        self._sent_eof = False
        self._seen_eof = False
        self._sent_reset = False
        self._seen_ack = False
        self._need_open = False
        self._need_eof = False
        self._need_reset = False
        self._need_ack = False
        self._read_cond = Condition()
        self._write_cond = Condition()
        self._flush_cond = Condition()
        self._read_closed_event = Event()
        self._write_closed_event = Event()
        self._acknowledged_event = Event()
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

    def _can_receive(self):
        return self._read_error is None

    def _schedule_ack(self, n):
        self._conn._add_window_ack(self._stream_id, n)

    def _schedule_ctrl(self):
        self._conn._add_ctrl_stream(self)

    def _schedule_data(self):
        self._conn._add_data_stream(self)

    def _clear_read_buffer(self):
        if self._readbuf:
            self._readbuf = 0
            self._cleanup()

    def _clear_write_buffer(self):
        self._writebuf = ''
        self._flush_cond.broadcast()
        if self._need_eof:
            self._schedule_ctrl()

    def _prepare_data(self, maxsize=0xffffff):
        n = len(self._writebuf)
        if self._writeleft < 0:
            n += self._writeleft
        if n > self._conn._writeleft:
            n = self._conn._writeleft
        if n > maxsize:
            n = maxsize
        if n > 0:
            data, self._writebuf = self._writebuf[:n], self._writebuf[n:]
            self._conn._writeleft -= len(data)
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
        if not self._writebuf and self._need_eof:
            if self._need_reset:
                return self._reset_error is None or isinstance(self._reset_error, StreamClosedError)
            return True
        return False

    def _active_data(self):
        if self._writeleft >= 0:
            return len(self._writebuf) > 0
        return len(self._writebuf)+self._writeleft > 0

    def _active_reset(self):
        if self._need_reset:
            if self._sent_eof and self._seen_eof:
                self._need_reset = False
                return False
            if not self._seen_eof and not self._sent_reset:
                return True
            if self._reset_error is None or isinstance(self._reset_error, StreamClosedError):
                self._need_reset = False
                return False
            if not self._writebuf and self._need_eof:
                return True
            return False
        return False

    def _send_ctrl(self, writer):
        if not self._writebuf and self._need_open and self._need_eof and self._need_reset:
            self._need_reset = False
            self._need_open = False
            self._need_eof = False
            self._sent_reset = True
            self._seen_eof = True
            self._sent_eof = True
            self._cleanup()
            return
        if self._need_open or self._need_ack:
            data = self._prepare_data()
            writer.write_frame(DataFrame(
                self._stream_id,
                self._outgoing_flags(),
                data,
            ))
            if not self._writebuf:
                self._flush_cond.broadcast()
        if self._active_reset():
            error = self._reset_error
            if error is None:
                error = self._read_error
            if isinstance(error, ConnectionClosedError):
                error = ConnectionShutdownError()
            flags = 0
            if not self._sent_reset:
                flags |= FLAG_RESET_READ
            if not self._writebuf and self._need_eof:
                self._need_reset = False
                self._need_eof = False
                self._sent_eof = True
                self._cleanup()
                flags |= FLAG_RESET_WRITE
            self._sent_reset = True
            writer.write_frame(ResetFrame(
                self._stream_id,
                flags,
                error,
            ))
        if not self._writebuf and self._need_eof:
            writer.write_frame(DataFrame(
                self._stream_id,
                self._outgoing_flags(),
                '',
            ))

    def _send_data(self, writer):
        data = self._prepare_data()
        if len(data) > 0:
            writer.write_frame(DataFrame(
                self._stream_id,
                self._outgoing_flags(),
                data,
            ))
        if self._active_data():
            self._schedule_data()
        elif self._active_reset():
            self._schedule_ctrl()
        if not self._writebuf:
            self._flush_cond.broadcast()

    def _process_data_frame(self, frame):
        if self._seen_eof:
            raise InvalidStreamError('stream 0x%08x cannot have DATA after EOF' % (self._stream_id))
        if frame.flags & FLAG_DATA_ACK:
            self._seen_ack = True
            self._acknowledged_event.set()
        if len(frame.data) > self._readleft:
            raise WindowOverflowError('stream 0x%08x received %d+%d bytes, which is more than %d bytes window' % (self._stream_id, len(self._readbuf), len(frame.data), self._readleft))
        if frame.data:
            if self._read_error is None:
                self._readbuf += frame.data
                self._read_cond.broadcast()
            self._readleft -= len(frame.data)
        if frame.flags & FLAG_DATA_EOF:
            self._seen_eof = True
            self._set_read_error(EOFError())
            self._cleanup()

    def _process_reset_frame(self, frame):
        if frame.flags & FLAG_RESET_READ:
            self._clear_write_buffer()
            self._set_write_error(frame.error)
        if frame.flags & FLAG_RESET_WRITE:
            error = frame.error
            if isinstance(error, StreamClosedError):
                error = EOFError()
            self._seen_eof = True
            self._set_read_error(error)
            self._cleanup()

    def _process_window_frame(self, frame):
        if frame.increment <= 0:
            raise InvalidFrameError('stream 0x%08x received invalid increment %d' % (self._stream_id, frame.increment))
        if self._write_error is None:
            self._writeleft += frame.increment
            if self._active_data():
                self._schedule_data()
            if self._writeleft > 0:
                self._write_cond.broadcast()

    def _set_read_error(self, error):
        if self._read_error is None:
            self._read_error = error
            self._read_closed_event.set()
            self._acknowledged_event.set()
            self._read_cond.broadcast()

    def _set_write_error(self, error):
        if self._write_error is None:
            self._write_error = error
            self._write_closed_event.set()
            self._writeleft = 0
            self._need_eof = True
            self._write_cond.broadcast()
            if not self._writebuf:
			    # We had no pending data, but now we need to send a EOF, which
                # can only be done in a ctrl phase, so make sure to schedule it.
                self._schedule_ctrl()

    def _reset_read_side(self):
        if not self._seen_eof:
            self._need_reset = True
            if self._active_reset():
                self._schedule_ctrl()

    def _reset_both_sides(self):
        if not self._seen_eof or not self._sent_eof:
            self._need_reset = True
            if self._active_reset():
                self._schedule_ctrl()

    def _close_with_error(self, error, closed):
        if error is None or isinstance(error, EOFError):
            error = StreamClosedError()
        if self._reset_error is None:
            self._reset_error = error
            self._set_read_error(error)
            self._set_write_error(error)
            self._flush_cond.broadcast()
        if closed:
            self._clear_read_buffer()
            self._reset_both_sides()

    @property
    def closed(self):
        return self._reset_error is not None

    def close(self):
        self._close_with_error(StreamClosedError(), True)

    def close_read(self):
        self._set_read_error(StreamClosedError())
        self._clear_read_buffer()
        self._reset_read_side()

    def close_read_error(self, error):
        if error is None or isinstance(error, EOFError):
            error = StreamClosedError()
        self._set_read_error(error)
        self._clear_read_buffer()
        self._reset_read_side()

    def close_write(self):
        self._set_write_error(StreamClosedError())

    def close_with_error(self, error):
        self._close_with_error(error, True)

    def peek(self):
        while not self._readbuf:
            if self._read_error is not None:
                if isinstance(self._read_error, EOFError):
                    return ''
                raise self._read_error
            self._read_cond.wait()
        return self._readbuf

    def discard(self, n):
        if n <= 0:
            return 0
        elif len(self._readbuf) <= n:
            n = len(self._readbuf)
            self._readbuf = ''
        else:
            self._readbuf = self._readbuf[n:]
        if self._read_error is None:
            self._readleft += n
            self._schedule_ack(n)
        return n

    def recv(self, bufsize):
        if bufsize <= 0:
            # Don't block if trying to receive 0 bytes
            return ''
        while not self._readbuf:
            if self._read_error is not None:
                if isinstance(self._read_error, EOFError):
                    return ''
                raise self._read_error
            self._read_cond.wait()
        if len(self._readbuf) <= bufsize:
            data, self._readbuf = self._readbuf, ''
        else:
            data, self._readbuf = self._readbuf[:bufsize], self._readbuf[bufsize:]
        if self._read_error is None:
            self._readleft += len(data)
            self._schedule_ack(len(data))
        return data

    def read(self, n=-1):
        data = ''
        while n != 0:
            while not self._readbuf:
                if self._read_error is not None:
                    if isinstance(self._read_error, EOFError):
                        return data
                    raise self._read_error
                self._read_cond.wait()
            if n < 0 or len(self._readbuf) <= n:
                chunk, self._readbuf = self._readbuf, ''
            else:
                chunk, self._readbuf = self._readbuf[:n], self._readbuf[n:]
            if self._read_error is None:
                self._readleft += len(chunk)
                self._schedule_ack(len(chunk))
            data += chunk
            if n > 0:
                n -= len(chunk)
        return data

    def readline(self, n=-1):
        data = ''
        while n != 0:
            while not self._readbuf:
                if self._read_error is not None:
                    if isinstance(self._read_error, EOFError):
                        return data
                    raise self._read_error
                self._read_cond.wait()
            size = self._readbuf.find('\n') + 1
            if size > 0:
                stop = True
            else:
                size = len(self._readbuf)
                stop = False
            if n > 0 and size >= n:
                size = n
                stop = True
            chunk, self._readbuf = self._readbuf[:size], self._readbuf[size:]
            if self._read_error is None:
                self._readleft += size
                self._schedule_ack(size)
            data += chunk
            if stop:
                break
            if n > 0:
                n -= size
        return data

    def send(self, data):
        while self._write_error is None and self._writeleft <= 0:
            self._write_cond.wait()
        if self._write_error is not None:
            raise self._write_error
        chunk = data[:self._writeleft]
        self._writebuf += chunk
        self._schedule_data()
        return len(chunk)

    def write(self, data):
        while data:
            n = self.send(data)
            data = data[n:]

    sendall = write

    def getpeername(self):
        return self._conn._sock.getpeername()

    def getsockname(self):
        return self._conn._sock.getsockname()

    def flush(self):
        while self._writebuf:
            if self._reset_error is not None:
                break
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
