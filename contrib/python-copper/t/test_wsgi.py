# -*- coding: utf-8 -*-
from copper.wsgi_support import wsgi

def test_http_handler(copper_client, copper_http_client):
    def application(environ, start_response):
        message = 'Hello, %s!' % (environ['PATH_INFO'],)
        start_response('200 OK', [
            ('Content-Type', 'text/plain; charset=UTF-8'),
            ('Content-Length', '%d' % len(message)),
        ])
        return [message]
    with copper_client.publish('http:hello', wsgi(application)):
        result = copper_http_client.open('copper:///hello/world').read()
        assert result == 'Hello, /world!'
        result = copper_http_client.open('copper:///hello/foobar').read()
        assert result == 'Hello, /foobar!'
        res = copper_http_client.open('copper:///hello')
        assert res.code == 404
        res = copper_http_client.open('copper:///foobar')
        assert res.code == 404
