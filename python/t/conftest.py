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
COPPERD_SRC = os.path.join(SRC_ROOT, 'cmd/copperd')
COPPERD_BIN = os.path.join(SRC_ROOT, 'bin/copperd')

@pytest.yield_fixture
def workdir():
    path = mkdtemp()
    try:
        yield path
    finally:
        rmtree(path, True)

@pytest.yield_fixture
def copperd(workdir):
    if not os.path.isfile(COPPERD_BIN):
        try:
            os.makedirs(os.path.dirname(COPPERD_BIN))
        except OSError as e:
            if e.errno != EEXIST:
                raise
        subprocess.check_call(['go', 'build', '-o', COPPERD_BIN], shell=False, cwd=COPPERD_SRC)
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
    p = subprocess.Popen([COPPERD_BIN, '-config', confpath], shell=False, cwd=workdir)
    try:
        while not os.path.exists(sockpath):
            time.sleep(0.001)
            rc = p.poll()
            if rc is not None:
                raise RuntimeError('copperd exited with status %r' % (rc,))
        yield sockpath
    finally:
        if p.poll() is None:
            p.terminate()
            p.wait()
