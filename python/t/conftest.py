# -*- coding: utf-8 -*-
import os
import json
import time
import pytest
import subprocess
from errno import EEXIST
from shutil import rmtree
from tempfile import mkdtemp

SRC_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), '..', '..'))
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
    sockpath = os.path.join(workdir, 'copper.sock')
    confpath = os.path.join(workdir, 'copper.conf')
    config = {
        "listen": [
            {
                "net": "unix",
                "addr": sockpath,
                "allow-changes": True,
            },
        ],
    }
    with open(confpath, 'w') as f:
        # YAML parses valid JSON data
        json.dump(config, f)
    p = subprocess.Popen(['copper-node', '-config', confpath], shell=False, cwd=workdir)
    try:
        while not os.path.exists(sockpath):
            time.sleep(0.001)
            rc = p.poll()
            if rc is not None:
                raise RuntimeError('copper-node exited with status %r' % (rc,))
        yield sockpath
    finally:
        if p.poll() is None:
            p.terminate()
            p.wait()

@pytest.yield_fixture
def copper_client(copper_node):
    from copper.client import CopperClient
    with CopperClient(('unix', copper_node)) as client:
        yield client
