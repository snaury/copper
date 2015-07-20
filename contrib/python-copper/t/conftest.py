# -*- coding: utf-8 -*-
import os
import sys
import json
import time
import pytest
import subprocess
from errno import EEXIST
from shutil import rmtree
from tempfile import mkdtemp
from gevent import socket
from httplib import HTTPConnection
from urllib2 import build_opener, AbstractHTTPHandler

SRC_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), '..', '..', '..'))
SRC_COPPER_NODE = os.path.join(SRC_ROOT, 'cmd/copper-node')

subprocess.check_call(['go', 'install'], shell=False, cwd=SRC_COPPER_NODE)

@pytest.yield_fixture
def workdir():
    path = mkdtemp()
    try:
        yield path
    finally:
        rmtree(path, True)

@pytest.yield_fixture
def copper_node(workdir):
    logpath = os.path.join(workdir, 'copper.log')
    unixpath = os.path.join(workdir, 'copper.sock')
    httppath = os.path.join(workdir, 'copper.http')
    confpath = os.path.join(workdir, 'copper.conf')
    config = {
        "listen": [
            {
                "net": "unix",
                "type": "http",
                "addr": httppath,
            },
            {
                "net": "unix",
                "addr": unixpath,
                "allow-changes": True,
            },
        ],
    }
    with open(confpath, 'w') as f:
        # YAML parses valid JSON data
        json.dump(config, f)
    with open(logpath, 'wb') as logfile:
        p = subprocess.Popen(['copper-node', '-config=' + confpath], shell=False, cwd=workdir, stdout=logfile, stderr=logfile)
    try:
        while not os.path.exists(unixpath):
            time.sleep(0.001)
            rc = p.poll()
            if rc is not None:
                with open(logpath, 'rb') as logfile:
                    sys.stderr.write(logfile.read())
                raise RuntimeError('copper-node exited with status %r' % (rc,))
        yield {
            'unix': unixpath,
            'http': httppath,
        }
    finally:
        if p.poll() is None:
            p.terminate()
            p.wait()

@pytest.yield_fixture
def copper_client(copper_node):
    from copper.client import Client
    with Client(('unix', copper_node['unix'])) as client:
        yield client

class CopperHTTPConnection(HTTPConnection):
    def connect(self):
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            timeout = self.timeout
            if timeout is not socket._GLOBAL_DEFAULT_TIMEOUT:
                sock.settimeout(timeout)
            sock.connect(self.host)
        except:
            sock.close()
            raise
        self.sock = sock

class CopperHTTPHandler(AbstractHTTPHandler):
    def __init__(self, default_path):
        AbstractHTTPHandler.__init__(self)
        self.default_path = default_path

    def copper_open(self, req):
        host = req.get_host()
        if not host:
            req.host = self.default_path
        return self.do_open(CopperHTTPConnection, req)

    def docker_request(self, req):
        host = req.get_host()
        if not host:
            req.host = self.default_path
        return self.do_request_(req)

    def docker_response(self, req, res):
        return res

@pytest.fixture
def copper_http_client(copper_node):
    return build_opener(CopperHTTPHandler(copper_node['http']))
