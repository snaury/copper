# -*- coding: utf-8 -*-
import time
from gevent import spawn
from gevent import Timeout
from gevent.event import Event
from .frames import (
    FLAG_FIN,
    FLAG_ACK,
    FLAG_INC,
    Frame,
    PingFrame,
    OpenFrame,
    DataFrame,
    ResetFrame,
    WindowFrame,
    SettingsFrame,
)
from .errors import (
    ConnectionTimeoutError,
    InvalidFrameError,
)

INACTIVITY_TIMEOUT = 60

__all__ = [
    'RawConn',
]

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

class RawConn(object):
    def __init__(self, sock, handler):
        self._sock = sock
        self._handler = handler
        self._reader = _RawConnReader(sock)
        self._writer = _RawConnWriter(sock)
        self._failure = None
        self._failure_out = None
        self._signal_close = Event()
        self._signal_write = Event()
        self._ping_acks = []
        self._ping_reqs = []
        self._window_acks = {}
        self._ctrl_streams = set()
        self._data_streams = set()
        self._streams = {}
        self._deadstreams = set()
        spawn(self._readloop)
        spawn(self._writeloop)

    def _close(self, error):
        if self._failure is None:
            self._failure = error
            if self._failure_out is not None:
                self._failure_out = error
            for stream in self._streams.values():
                stream._close(error)
            self._signal_close.set()
            self._signal_write.set()

    def _remove_stream(self, stream):
        stream_id = stream._stream_id
        if self._streams.get(stream_id) is stream:
            del self._streams[stream_id]
            self._ctrl_streams.discard(stream)
            self._data_streams.discard(stream)
            if stream_id & 1:
                # This is a client stream
                self._deadstreams.add(stream_id)

    def _process_ping_frame(self, frame):
        pass

    def _process_open_frame(self, frame):
        pass

    def _process_data_frame(self, frame):
        pass

    def _process_reset_frame(self, frame):
        pass

    def _process_window_frame(self, frame):
        pass

    def _process_settings_frame(self, frame):
        pass

    _process_frame_by_type = {
        PingFrame: _process_ping_frame,
        OpenFrame: _process_open_frame,
        DataFrame: _process_data_frame,
        ResetFrame: _process_reset_frame,
        WindowFrame: _process_window_frame,
        SettingsFrame: _process_settings_frame,
    }

    def _readloop(self):
        while True:
            try:
                frame = None
                with Timeout(INACTIVITY_TIMEOUT, False):
                    frame = Frame.load(self._reader)
                if frame is None:
                    raise ConnectionTimeoutError()
                process = self._process_frame_by_type.get(frame.__class__)
                if process is None:
                    raise InvalidFrameError('received an unsupported frame')
            except Exception as e:
                self._close(e)
                break

    def _stream_can_receive(self, stream_id):
        if stream_id == 0:
            return True
        stream = self._streams.get(stream_id)
        if stream is not None and stream._can_receive():
            return True
        return False

    def _add_window_ack(self, stream_id, increment):
        self._window_acks[stream_id] = self._window_acks.get(stream_id, 0) + increment
        self._signal_write.set()

    def _add_ctrl_stream(self, stream):
        self._ctrl_streams.add(stream)
        self._signal_write.set()

    def _add_data_stream(self, stream):
        self._data_streams.add(stream)
        self._signal_write.set()

    def _write_frames(self):
        while True:
            self._signal_write.clear()
            sent_marker = self._writer.sent_marker
            send_detected = False
            if self._ping_acks:
                ping_acks, self._ping_acks = self._ping_acks, []
                for value in ping_acks:
                    PingFrame(FLAG_ACK, value).dump(self._writer)
                if sent_marker is not self._writer.sent_marker:
                    continue
            if self._ping_reqs:
                ping_reqs, self._ping_reqs = self._ping_reqs, []
                for value in ping_reqs:
                    PingFrame(0, value).dump(self._writer)
                if sent_marker is not self._writer.sent_marker:
                    continue
            # TODO: settings frames
            while self._window_acks:
                stream_id, increment = self._window_acks.popitem()
                flags = FLAG_ACK
                if self._stream_can_receive(stream_id):
                    flags |= FLAG_INC
                WindowFrame(stream_id, flags, increment).dump(self._writer)
                if sent_marker is not self._writer.sent_marker:
                    send_detected = True
                    break
            if send_detected:
                continue
            if self._failure_out is not None:
                ResetFrame(0, FLAG_FIN, self._failure_out).dump(self._writer)
                self._writer.flush()
                return False
            while self._ctrl_streams:
                stream = self._ctrl_streams.pop()
                stream._send_ctrl()
                if sent_marker is not self._writer.sent_marker:
                    send_detected = True
                    break
            if send_detected:
                continue
            while self._data_streams:
                stream = self._data_streams.pop()
                stream._send_data()
                if sent_marker is not self._writer.sent_marker:
                    send_detected = True
                    break
            if send_detected:
                continue
            if self._signal_write.is_set():
                # signal channel active, must try again
                continue
            # we are done sending stuff
            break
        return True

    def _writeloop(self):
        nextdata = time.time() + INACTIVITY_TIMEOUT * 2 // 3
        while True:
            datarequired = True
            delay = nextdata - time.time()
            if delay > 0:
                with Timeout(delay, False):
                    self._signal_write.wait()
                    datarequired = False
            try:
                initial_sent_marker = self._writer.sent_marker
                if not self._write_frames():
                    break
                if datarequired and initial_sent_marker is self._writer.sent_marker and not self._writer.buffer:
                    # we must send an empty data frame, however we haven't sent anything yet
                    DataFrame(0, 0, '').dump(self._writer)
                self._writer.flush()
                if initial_sent_marker is not self._writer.sent_marker:
                    # we have sent some data, restart the timer
                    nextdata = time.time() + INACTIVITY_TIMEOUT * 2 // 3
            except Exception as e:
                self._close(e)
                break

class RawStream(object):
    def __init__(self, conn, stream_id, target_id, data='', iseof=False, isoutgoing=True):
        self._conn = conn
        self._stream_id = stream_id
        self._target_id = target_id
        self._readbuf = ''
        self._writebuf = data
        self._writenack = 0
        self._writeleft = 0
        self._sent_eof = False
        self._seen_eof = iseof
        self._sent_reset = False
        self._need_open = isoutgoing
        self._need_eof = False
        self._need_reset = False
        self._read_error = None
        self._write_error = None
        self._read_event = Event()
        self._write_event = Event()

    def _can_receive(self):
        return self._read_error is None

    def _fully_closed(self):
        return self._sent_eof and self._seen_eof and not self._readbuf and self._writenack <= 0

    def _clear_read_buffer(self):
        if self._readbuf:
            self._readbuf = 0
            if self._fully_closed():
                self._conn._remove_stream(self)

    def recv(self, n):
        while not self._readbuf:
            if self._read_error is not None:
                if isinstance(self._read_error, EOFError):
                    return ''
                raise self._read_error
            self._read_event.clear()
            self._read_event.wait()
        if n > len(self._readbuf):
            data, self._readbuf = self._readbuf, ''
        elif n > 0:
            data, self._readbuf = self._readbuf[:n], self._readbuf[n:]
        else:
            data = ''
        if len(data) > 0:
            self._conn._add_window_ack(self._stream_id, len(data))
        return data

    def read(self, n):
        data = ''
        while True:
            chunk = self.recv(n - len(data))
            if not chunk:
                break
            data += chunk
            if len(data) == n:
                break
        return data

    def send(self, data):
        while self._write_error is None and self._writeleft <= 0:
            self._write_event.clear()
            self._write_event.wait()
        if self._write_error is not None:
            raise self._write_error
        chunk = data[:self._writeleft]
        self._writebuf += chunk
        self._conn._add_data_stream(self)
        return len(chunk)

    def write(self, data):
        while data:
            n = self.send(data)
            data = data[n:]
