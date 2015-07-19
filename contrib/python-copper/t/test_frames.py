# -*- coding: utf-8 -*-
from copper.frames import (
    Frame,
    PingFrame,
    DataFrame,
    ResetFrame,
    WindowFrame,
    SettingsFrame,
    FLAG_PING_ACK,
    FLAG_DATA_OPEN,
    FLAG_DATA_EOF,
    FLAG_RESET_READ,
    FLAG_RESET_WRITE,
    FLAG_SETTINGS_ACK,
)
from copper.errors import (
    InternalError,
    InvalidDataError,
)

def to_binary(values):
    def encode_value(value):
        if isinstance(value, str):
            return value
        return chr(value)
    return ''.join(encode_value(value) for value in values)

FRAMES = [
    PingFrame(0, 0x1122334455667788),
    PingFrame(FLAG_PING_ACK, 0x1122334455667788),
    DataFrame(0x42, FLAG_DATA_OPEN, '\xff\xfe\xfd\xfc\xfb\xfa\xf9\xf8'),
    DataFrame(0x42, FLAG_DATA_EOF, '\xff\xfe\xfd\xfc\xfb\xfa\xf9\xf8'),
    ResetFrame(0x42, FLAG_RESET_READ|FLAG_RESET_WRITE, InvalidDataError()),
    ResetFrame(0x42, FLAG_RESET_READ|FLAG_RESET_WRITE, InvalidDataError('test')),
    WindowFrame(0x42, 0x25, 0x11223344),
    SettingsFrame(0, {1: 7, 2: 6}),
    SettingsFrame(FLAG_SETTINGS_ACK, {}),
    ResetFrame(0, 0, InternalError()),
]

BINARY = to_binary([
    # a PING frame
    0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00,
    0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
    # a PING ack frame
    0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x08, 0x01, 0x00,
    0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88,
    # a DATA frame
    0x00, 0x00, 0x00, 0x42, 0x00, 0x00, 0x08, 0x02, 0x01,
    0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
    # a DATA frame
    0x00, 0x00, 0x00, 0x42, 0x00, 0x00, 0x08, 0x01, 0x01,
    0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
    # a RESET frame
    0x00, 0x00, 0x00, 0x42, 0x00, 0x00, 0x04, 0x03, 0x02,
    0x00, 0x00, 0x00, 0x65,
    # a RESET frame + message
    0x00, 0x00, 0x00, 0x42, 0x00, 0x00, 0x08, 0x03, 0x02,
    0x00, 0x00, 0x00, 0x65,
    't', 'e', 's', 't',
    # a WINDOW frame
    0x00, 0x00, 0x00, 0x42, 0x00, 0x00, 0x04, 0x25, 0x03,
    0x11, 0x22, 0x33, 0x44,
    # a SETTINGS frame
    0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x0c, 0x00, 0x04,
    0x00, 0x01, 0x00, 0x00, 0x00, 0x07,
    0x00, 0x02, 0x00, 0x00, 0x00, 0x06,
    # a SETTINGS ack frame
    0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x04,
    # a RESET frame with a EUNKNOWN error
    0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x02,
    0x00, 0x00, 0x00, 0x01,
])

class TestReader(object):
    def __init__(self, data):
        self.data = data

    def peek(self):
        return self.data

    def read(self, n):
        if len(self.data) < n:
            raise EOFError()
        data, self.data = self.data[:n], self.data[n:]
        return data

def test_decode_frames():
    frames = list(FRAMES)
    reader = TestReader(BINARY)
    while True:
        frame = Frame.load(reader)
        if frame is None:
            break
        assert frames, 'Unexpected frame %r' % (frame,)
        expected, frames = frames[0], frames[1:]
        assert frame == expected
    assert not frames, 'Expected frame %r' % (frames[0],)

class TestWriter(object):
    def __init__(self):
        self.data = ''

    def write(self, data):
        self.data += data

    def flush(self):
        pass

def test_encode_frames():
    writer = TestWriter()
    for frame in FRAMES:
        frame.dump(writer)
    assert writer.data.encode('hex') == BINARY.encode('hex')
