from copper.frames import (
    FLAG_FIN,
    FLAG_ACK,
    Frame,
    PingFrame,
    OpenFrame,
    DataFrame,
    ResetFrame,
    WindowFrame,
    SettingsFrame,
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
    PingFrame(FLAG_ACK, 0x1122334455667788),
    OpenFrame(0x42, 0x25, 0x1122334455667788, '\xff\xfe\xfd\xfc\xfb\xfa\xf9\xf8'),
    DataFrame(0x42, 0x25, '\xff\xfe\xfd\xfc\xfb\xfa\xf9\xf8'),
    ResetFrame(0x42, 0x25, InvalidDataError()),
    ResetFrame(0x42, 0x25, InvalidDataError('test')),
    WindowFrame(0x42, 0x25, 0x11223344),
    SettingsFrame(0, {2: 3}),
    SettingsFrame(FLAG_ACK, {}),
    ResetFrame(0, 0, InternalError()),
]

BINARY = to_binary([
    # a PING frame
    0x00, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00,
    0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11,
    # a PING ack frame
    0x00, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x01, 0x00,
    0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11,
    # an OPEN frame
    0x42, 0x00, 0x00, 0x00, 0x10, 0x00, 0x00, 0x25, 0x01,
    0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11,
    0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
    # a DATA frame
    0x42, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x25, 0x02,
    0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8,
    # a RESET frame
    0x42, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x25, 0x03,
    0x65, 0x00, 0x00, 0x00,
    # a RESET frame + message
    0x42, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x25, 0x03,
    0x65, 0x00, 0x00, 0x00,
    't', 'e', 's', 't',
    # a WINDOW frame
    0x42, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x25, 0x04,
    0x44, 0x33, 0x22, 0x11,
    # a SETTINGS frame
    0x00, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x05,
    0x02, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00,
    # a SETTINGS ack frame
    0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x05,
    # a RESET frame with a EUNKNOWN error
    0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x03,
    0x01, 0x00, 0x00, 0x00,
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