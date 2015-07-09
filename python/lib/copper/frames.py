# -*- coding: utf-8 -*-
import struct
from .errors import CopperError, UnknownFrameError, InvalidFrameError

FRAME_HEADER_FMT = struct.Struct('<IIB')
assert FRAME_HEADER_FMT.size == 9

PING_FRAME_ID = 0
OPEN_FRAME_ID = 1
DATA_FRAME_ID = 2
RESET_FRAME_ID = 3
WINDOW_FRAME_ID = 4
SETTINGS_FRAME_ID = 5

FLAG_FIN = 1 # OPEN, DATA, RESET
FLAG_ACK = 1 # PING, WINDOW, SETTINGS
FLAG_INC = 2 # WINDOW

class Header(object):
    __slots__ = ('stream_id', 'payload_size', 'flags', 'kind')

    def __init__(self, stream_id, payload_size, flags, kind):
        self.stream_id = stream_id
        self.payload_size = payload_size
        self.flags = flags
        self.kind = kind

    @classmethod
    def load(cls, reader):
        stream_id, flags_size, kind = FRAME_HEADER_FMT.unpack(reader.read(9))
        return cls(stream_id, flags_size & 0xffffff, flags_size >> 24, kind)

    def dump(self, writer):
        writer.write(FRAME_HEADER_FMT.pack(self.stream_id, (self.flags << 24) | self.payload_size, self.kind))

class PingFrame(object):
    __slots__ = ('flags', 'value')

    FMT = struct.Struct('<Q')

    def __init__(self, flags, value):
        self.flags = flags
        self.value = value

    @classmethod
    def load(cls, header, reader):
        if header.stream_id != 0 or header.payload_size != 8:
            raise InvalidFrameError()
        value, = cls.FMT.unpack(reader.read(8))
        return cls(header.flags, value)

    def dump(self, writer):
        Header(0, 8, self.flags, PING_FRAME_ID).dump(writer)
        writer.write(self.FMT.pack(self.value))

class OpenFrame(object):
    __slots__ = ('stream_id', 'flags', 'target_id', 'data')

    FMT = struct.Struct('<Q')

    def __init__(self, stream_id, flags, target_id, data):
        self.stream_id = stream_id
        self.flags = flags
        self.target_id = target_id
        self.data = data

    @classmethod
    def load(cls, header, reader):
        if header.payload_size < 8:
            raise InvalidFrameError()
        target_id, = cls.FMT.unpack(reader.read(8))
        if header.payload_size > 8:
            data = reader.read(header.payload_size - 8)
        else:
            data = ''
        return cls(header.stream_id, header.flags, target_id, data)

    def dump(self, writer):
        Header(self.stream_id, 8 + len(self.data), self.flags, OPEN_FRAME_ID).dump(writer)
        writer.write(self.FMT.pack(self.target_id))
        if self.data:
            writer.write(self.data)

class DataFrame(object):
    __slots__ = ('stream_id', 'flags', 'data')

    def __init__(self, stream_id, flags, data):
        self.stream_id = stream_id
        self.flags = flags
        self.data = data

    @classmethod
    def load(cls, header, reader):
        if header.payload_size > 0:
            data = reader.read(header.payload_size)
        else:
            data = ''
        return cls(header.stream_id, header.flags, data)

    def dump(self, writer):
        Header(self.stream_id, len(self.data), self.flags, DATA_FRAME_ID).dump(writer)
        if self.data:
            writer.write(self.data)

class ResetFrame(object):
    __slots__ = ('stream_id', 'flags', 'error')

    FMT = struct.Struct('<i')

    def __init__(self, stream_id, flags, error):
        self.stream_id = stream_id
        self.flags = flags
        self.error = error

    @classmethod
    def load(cls, header, reader):
        if header.payload_size < 4:
            raise InvalidFrameError()
        error_code = cls.FMT.unpack(reader.read(4))
        if header.payload_size > 4:
            message = reader.read(header.payload_size - 4)
        else:
            message = ''
        return cls(header.stream_id, header.flags, CopperError.from_error_code(error_code, message))

    def dump(self, writer):
        error_code = getattr(self.error, 'copper_error', -1)
        if error_code == -1:
            message = '%s' % (self.error,)
        else:
            message = self.error.message or ''
        if not isinstance(message, basestring):
            try:
                message = str(message)
            except UnicodeError:
                message = unicode(message)
        if isinstance(message, unicode):
            message = message.encode('utf8')
        Header(self.stream_id, len(message) + 4, self.flags, RESET_FRAME_ID).dump(writer)
        writer.write(self.FMT.pack(error_code))
        if message:
            writer.write(message)

class WindowFrame(object):
    __slots__ = ('stream_id', 'flags', 'increment')

    FMT = struct.Struct('<I')

    def __init__(self, stream_id, flags, increment):
        self.stream_id = stream_id
        self.flags = flags
        self.increment = increment

    @classmethod
    def load(cls, header, reader):
        if header.payload_size != 4:
            raise InvalidFrameError()
        increment, = cls.FMT.unpack(reader.read(4))
        return cls(header.stream_id, header.flags, increment)

    def dump(self, writer):
        Header(self.stream_id, 4, self.flags, WINDOW_FRAME_ID).dump(writer)
        writer.write(self.FMT.pack(self.increment))

class SettingsFrame(object):
    __slots__ = ('flags', 'values')

    FMT = struct.Struct('<II')

    def __init__(self, flags, values):
        self.flags = flags
        self.values = values

    @classmethod
    def load(cls, header, reader):
        if header.stream_id != 0:
            raise InvalidFrameError()
        if header.flags & FLAG_ACK:
            if header.payload_size != 0:
                raise InvalidFrameError()
            values = {}
        else:
            if (header.payload_size % 8) != 0:
                raise InvalidFrameError()
            count = header.payload_size // 8
            values = {}
            while count > 0:
                sid, value = cls.FMT.unpack(reader.read(8))
                values[sid] = value
                count -= 1
        return cls(header.flags, values)

    def dump(self, writer):
        Header(0, 8 * len(self.values), self.flags, SETTINGS_FRAME_ID).dump(writer)
        for sid, value in self.values.iteritems():
            writer.write(self.FMT.pack(sid, value))

FRAME_CLASSES = {
    PING_FRAME_ID: PingFrame,
    OPEN_FRAME_ID: OpenFrame,
    DATA_FRAME_ID: DataFrame,
    RESET_FRAME_ID: ResetFrame,
    WINDOW_FRAME_ID: WindowFrame,
    SETTINGS_FRAME_ID: SettingsFrame,
}

def read_frame(reader):
    if not reader.peek():
        return None
    header = Header.load(reader)
    cls = FRAME_CLASSES.get(header.kind)
    if cls is None:
        raise UnknownFrameError()
    return cls.load(header, reader)
