# -*- coding: utf-8 -*-
__all__ = [
    'CopperError',
]

class CopperErrorMeta(type):
    def __new__(meta, name, bases, bodydict):
        cls = type.__new__(meta, name, bases, bodydict)
        copper_error = bodydict.get('copper_error')
        if copper_error is not None:
            classes_by_code = bodydict.get('classes_by_code')
            if classes_by_code is None:
                classes_by_code = CopperError.classes_by_code
            prev = meta.registered.get(copper_error)
            if prev is not None:
                raise TypeError('Errors %s and %s have the same error code' % (prev.__name__, name))
            meta.registered[copper_error] = cls
        return cls

class CopperError(Exception):
    __metaclass__ = CopperErrorMeta

    copper_error = None
    classes_by_code = {}

    @classmethod
    def from_error_code(cls, code, message=''):
        impl = cls.classes_by_code.get(code)
        if impl is not None:
            result = impl(message)
        else:
            result = cls(message)
            result.copper_error = code
        return result

    def __str__(self):
        message = self.message
        if not message:
            docstring = self.__type__.__doc__
            if docstring:
                message = docstring
            else:
                copper_error = getattr(self, 'copper_error', None)
                if copper_error is not None:
                    message = 'ERROR_%s' % (copper_error,)
        return message

class InternalError(CopperError):
    """internal error"""
    copper_error = 1

class StreamClosedError(CopperError):
    """stream closed"""
    copper_error = 100

class InvalidDataError(CopperError):
    """data is not valid"""
    copper_error = 101

class TimeoutError(CopperError):
    """operation timed out"""
    copper_error = 102

class NoRouteError(CopperError):
    """no route to target"""
    copper_error = 103

class NoTargetError(CopperError):
    """no such target"""
    copper_error = 104

class UnsupportedError(CopperError):
    """feature is not supported"""
    copper_error = 105

class OverCapacityError(CopperError):
    """server is over capacity"""
    copper_error = 106

class ConnectionClosedError(CopperError):
    """connection closed"""
    copper_error = 200

class ConnectionShutdownError(CopperError):
    """connection is shutting down"""
    copper_error = 201

class UnknownFrameError(CopperError):
    """unknown frame type"""
    copper_error = 202

class InvalidFrameError(CopperError):
    """invalid frame data"""
    copper_error = 203

class WindowOverflowError(CopperError):
    """receive window overflow"""
    copper_error = 204

class InvalidStreamError(CopperError):
    """received invalid stream id"""
    copper_error = 205

class UnknownStreamError(CopperError):
    """received unknown stream id"""
    copper_error = 206
