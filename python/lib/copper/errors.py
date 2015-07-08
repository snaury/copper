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

class UnknownError(CopperError):
    """unknown error"""
    copper_error = -1

class UnknownFrameError(CopperError):
    """unknown frame"""
    copper_error = 1

class InvalidFrameError(CopperError):
    """invalid frame"""
    copper_error = 2
