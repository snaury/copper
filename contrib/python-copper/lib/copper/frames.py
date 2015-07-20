# -*- coding: utf-8 -*-
import struct
from .errors import (
    CopperError,
    UnknownFrameError,
    InvalidFrameError,
    InternalError,
)

__all__ = [
    'Frame',
    'PingFrame',
    'OpenFrame',
    'DataFrame',
    'ResetFrame',
    'WindowFrame',
    'SettingsFrame',
]

FRAME_HEADER_FMT = struct.Struct('>IIB')
assert FRAME_HEADER_FMT.size == 9

FLAG_PING_ACK = 1
FLAG_DATA_EOF = 1
FLAG_DATA_OPEN = 2
FLAG_DATA_ACK = 4
FLAG_RESET_READ = 1
FLAG_RESET_WRITE = 2
FLAG_SETTINGS_ACK = 1

class Header(object):
    __slots__ = ('stream_id', 'payload_size', 'flags', 'kind')

    def __init__(self, stream_id, payload_size, flags, kind):
        self.stream_id = stream_id
        self.payload_size = payload_size
        self.flags = flags
        self.kind = kind

    @classmethod
    def load(cls, reader):
        stream_id, size_flags, kind = FRAME_HEADER_FMT.unpack(reader.read(9))
        return cls(stream_id, size_flags >> 8, size_flags & 0xff, kind)

    def dump(self, writer):
        writer.write(FRAME_HEADER_FMT.pack(self.stream_id, self.flags | (self.payload_size << 8), self.kind))

class FrameMeta(type):
    def __new__(meta, name, bases, bodydict):
        cls = type.__new__(meta, name, bases, bodydict)
        frame_id = bodydict.get('ID')
        frame_classes = cls.frame_classes
        if frame_id is not None:
            prev = frame_classes.get(frame_id)
            if prev is not None:
                raise TypeError('Frames %s and %s have the same type id %r' % (prev.__name__, name, frame_id))
            frame_classes[frame_id] = cls
        return cls

class Frame(object):
    __slots__ = ()
    __metaclass__ = FrameMeta
    frame_classes = {}

    @classmethod
    def load(cls, reader):
        if not reader.peek():
            return None
        header = Header.load(reader)
        impl = cls.frame_classes.get(header.kind)
        if impl is None:
            raise UnknownFrameError()
        return impl.load_frame_data(header, reader)

    def __repr__(self):
        return '%s(%s)' % (
            self.__class__.__name__,
            ', '.join(
                '%s=%r' % (name, getattr(self, name))
                for name in self.__class__.__slots__,
            ),
        )

class PingFrame(Frame):
    __slots__ = ('flags', 'value')

    ID = 0
    FMT = struct.Struct('>q')

    def __init__(self, flags, value):
        self.flags = flags
        self.value = value

    def __cmp__(self, other):
        if other is self:
            return 0
        if isinstance(other, PingFrame):
            return cmp(
                (self.flags, self.value),
                (other.flags, other.value),
            )
        return cmp(id(self), id(other))

    @classmethod
    def load_frame_data(cls, header, reader):
        if header.stream_id != 0 or header.payload_size != 8:
            raise InvalidFrameError()
        value, = cls.FMT.unpack(reader.read(8))
        return cls(header.flags, value)

    def dump(self, writer):
        Header(0, 8, self.flags, self.ID).dump(writer)
        writer.write(self.FMT.pack(self.value))

class DataFrame(Frame):
    __slots__ = ('stream_id', 'flags', 'data')

    ID = 1

    def __init__(self, stream_id, flags, data):
        self.stream_id = stream_id
        self.flags = flags
        self.data = data

    def __cmp__(self, other):
        if other is self:
            return 0
        if isinstance(other, DataFrame):
            return cmp(
                (self.stream_id, self.flags, self.data),
                (other.stream_id, other.flags, other.data),
            )
        return cmp(id(self), id(other))

    @classmethod
    def load_frame_data(cls, header, reader):
        if header.payload_size > 0:
            data = reader.read(header.payload_size)
        else:
            data = ''
        return cls(header.stream_id, header.flags, data)

    def dump(self, writer):
        Header(self.stream_id, len(self.data), self.flags, self.ID).dump(writer)
        if self.data:
            writer.write(self.data)

class ResetFrame(Frame):
    __slots__ = ('stream_id', 'flags', 'error')

    ID = 2
    FMT = struct.Struct('>I')

    def __init__(self, stream_id, flags, error):
        self.stream_id = stream_id
        self.flags = flags
        self.error = error

    def __cmp__(self, other):
        if other is self:
            return 0
        if isinstance(other, ResetFrame):
            return cmp(
                (self.stream_id, self.flags, self.error),
                (other.stream_id, other.flags, other.error),
            )
        return cmp(id(self), id(other))

    @classmethod
    def load_frame_data(cls, header, reader):
        if header.payload_size < 4:
            raise InvalidFrameError()
        error_code, = cls.FMT.unpack(reader.read(4))
        if header.payload_size > 4:
            message = reader.read(header.payload_size - 4)
        else:
            message = ''
        return cls(header.stream_id, header.flags, CopperError.from_error_code(error_code, message))

    def dump(self, writer):
        error_code = getattr(self.error, 'copper_error', InternalError.copper_error)
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
        Header(self.stream_id, len(message) + 4, self.flags, self.ID).dump(writer)
        writer.write(self.FMT.pack(error_code))
        if message:
            writer.write(message)

class WindowFrame(Frame):
    __slots__ = ('stream_id', 'flags', 'increment')

    ID = 3
    FMT = struct.Struct('>I')

    def __init__(self, stream_id, flags, increment):
        self.stream_id = stream_id
        self.flags = flags
        self.increment = increment

    def __cmp__(self, other):
        if other is self:
            return 0
        if isinstance(other, WindowFrame):
            return cmp(
                (self.stream_id, self.flags, self.increment),
                (other.stream_id, other.flags, other.increment),
            )
        return cmp(id(self), id(other))

    @classmethod
    def load_frame_data(cls, header, reader):
        if header.payload_size != 4:
            raise InvalidFrameError()
        increment, = cls.FMT.unpack(reader.read(4))
        return cls(header.stream_id, header.flags, increment)

    def dump(self, writer):
        Header(self.stream_id, 4, self.flags, self.ID).dump(writer)
        writer.write(self.FMT.pack(self.increment))

class SettingsFrame(Frame):
    __slots__ = ('flags', 'values')

    ID = 4
    FMT = struct.Struct('>HI')

    def __init__(self, flags, values):
        self.flags = flags
        self.values = values

    def __cmp__(self, other):
        if other is self:
            return 0
        if isinstance(other, SettingsFrame):
            return cmp(
                (self.flags, self.values),
                (other.flags, other.values),
            )
        return cmp(id(self), id(other))

    @classmethod
    def load_frame_data(cls, header, reader):
        if header.stream_id != 0:
            raise InvalidFrameError()
        if header.flags & FLAG_SETTINGS_ACK:
            if header.payload_size != 0:
                raise InvalidFrameError()
            values = {}
        else:
            if (header.payload_size % 6) != 0:
                raise InvalidFrameError()
            count = header.payload_size // 6
            values = {}
            while count > 0:
                sid, value = cls.FMT.unpack(reader.read(6))
                values[sid] = value
                count -= 1
        return cls(header.flags, values)

    def dump(self, writer):
        Header(0, 6 * len(self.values), self.flags, self.ID).dump(writer)
        for sid, value in sorted(self.values.items()):
            writer.write(self.FMT.pack(sid, value))
