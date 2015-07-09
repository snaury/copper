#!/usr/bin/env python
# -*- coding: utf-8 -*-
from setuptools import setup
from setuptools.command.test import test as TestCommand

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
    package_dir = {'': 'lib'},
    packages = ['copper'],
    install_requires = ['setuptools', 'protobuf'],
    zip_safe = True,
    cmdclass = {
        'test': PyTest,
    },
)
