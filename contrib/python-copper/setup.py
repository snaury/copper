#!/usr/bin/env python
# -*- coding: utf-8 -*-
import os
import subprocess
from setuptools import setup
from setuptools.command.test import test as TestCommand

SRC_PROTOCOL = '../../protocol'

# Generated code built with newer versions of protobuf does not work with older
# versions of python-protobuf, so it's better to regenerate it every time.
subprocess.check_call([
    'protoc',
    '--python_out=lib/copper',
    '--proto_path=' + SRC_PROTOCOL,
    os.path.join(SRC_PROTOCOL, 'copper.proto'),
])

class PyTest(TestCommand):
    user_options = [('pytest-args=', 'a', "Arguments to pass to py.test")]

    def initialize_options(self):
        TestCommand.initialize_options(self)
        self.pytest_args = []

    def finalize_options(self):
        TestCommand.finalize_options(self)
        self.test_args = []
        self.test_suite = True

    def run_tests(self):
        import sys, pytest
        errno = pytest.main(self.pytest_args)
        sys.exit(errno)

setup(
    name = 'copper',
    version = '0.0.1',
    author = 'Alexey Borzenkov',
    author_email = 'snaury@gmail.com',
    description = 'Copper client library',
    url = 'https://github.com/snaury/copper',
    license = 'MIT License',
    package_dir = {'': 'lib'},
    packages = ['copper'],
    install_requires = ['setuptools', 'protobuf', 'gevent'],
    tests_require = ['pytest', 'pytest-capturelog'],
    cmdclass = {
        'test': PyTest,
    },
)
