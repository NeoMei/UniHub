"""Exercise the shipped locations against local upstreams using stock nginx with auth_request."""
from contextlib import ExitStack
from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import os
import socket
import subprocess
import tempfile
import threading
import time

root = Path(__file__).resolve().parent
seen = []


class Upstream(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        seen.append((self.path, self.headers))
        self.send_response(401 if self.path.endswith('/denied') else 200)
        self.send_header('X-Frame-Options', 'DENY')
        self.send_header('Content-Security-Policy', "frame-ancestors 'none'")
        self.end_headers()
        self.wfile.write(b'native')


def serve(stack):
    server = ThreadingHTTPServer(('127.0.0.1', 0), Upstream)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    stack.callback(server.server_close)
    stack.callback(server.shutdown)
    return server.server_port


def main():
    nginx = os.environ.get('NGINX_BIN', 'nginx')
    with ExitStack() as stack:
        directory = Path(stack.enter_context(tempfile.TemporaryDirectory()))
        conf = directory / 'logto'
        conf.mkdir()
        (directory / 'logs').mkdir()
        admin = serve(stack)
        management = serve(stack)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        for name in ('cookies.http.conf', 'locations.conf', 'management.inc', 'proxy.inc'):
            data = (root / name).read_text().replace('127.0.0.1:13003', f'127.0.0.1:{admin}').replace('127.0.0.1:13004', f'127.0.0.1:{management}')
            (conf / name).write_text(data)
        (directory / 'nginx.conf').write_text('''
events {}
http {
    access_log off;
    include logto/cookies.http.conf;
    server {
        listen 127.0.0.1:PORT;
        location = /_unihub_operator {
            internal;
            if ($cookie_unihub_session = op) { return 204; }
            if ($cookie_unihub_session = member) { return 403; }
            return 401;
        }
        location @unihub_portal_login { return 401 'login'; }
        location = /auth/callback { return 200 'UniHub'; }
        include logto/locations.conf;
        location / { return 404; }
    }
}
'''.replace('PORT', str(port)))
        args = [nginx, '-p', str(directory) + '/', '-c', 'nginx.conf']
        subprocess.run(args + ['-t'], check=True)
        process = subprocess.Popen(args + ['-g', 'daemon off;'])
        stack.callback(process.wait)
        stack.callback(process.terminate)

        def get(path, cookies='', **extra):
            conn = HTTPConnection('127.0.0.1', port, timeout=4)
            try:
                conn.request('GET', path, headers={'Cookie': cookies, **extra})
                response = conn.getresponse()
                return response.status, response.read(), response.headers
            finally:
                conn.close()

        for _ in range(100):
            try:
                get('/ready')
                break
            except ConnectionRefusedError:
                time.sleep(0.02)
        for path in ('/auth', '/auth/', '/auth/console/users', '/auth/oidc/jwks',
                     '/auth/management/api/users', '/auth/management/oidc/.well-known/openid-configuration'):
            assert get(path)[:2] == (401, b'login')
            assert get(path, 'unihub_session=member')[0] == 403
        assert get('/auth/callback?code=fixture')[:2] == (200, b'UniHub')
        assert get('/authentic')[0] == 404
        assert not seen
        cookies = 'unihub_session=op; _session_id=foreign; _SESSION=wrongcase; other=deny; _session=abc==; _session.sig=s; _interaction_resume=r; _logto={"appId":"admin-console"}'
        status, body, headers = get('/auth/denied', cookies, **{
            'X-Forwarded-Host': 'foreign.example', 'Authorization': 'Basic private', 'X-UniHub-Token': 'private'})
        assert (status, body) == (401, b'native')
        assert headers['X-Frame-Options'] == 'DENY' and headers['Content-Security-Policy'] == "frame-ancestors 'none'"
        uri, headers = seen.pop()
        assert uri == '/auth/denied' and headers['Host'] == 'localhost:13003'
        assert headers['X-Forwarded-Host'] == 'unihub.quukk.com' and headers['X-Forwarded-Proto'] == 'https'
        assert set(headers['Cookie'].split('; ')) == {'_interaction_resume=r', '_session=abc==', '_session.sig=s', '_logto={"appId":"admin-console"}'}
        assert not headers['Authorization'] and not headers['X-UniHub-Token']
        assert get('/auth/console', 'unihub_session=op')[0] == 200
        assert not seen.pop()[1]['Cookie']
        assert get('/auth/management/api/users?page=2', cookies, Authorization='Bearer fixture.jwt')[0] == 200
        uri, headers = seen.pop()
        assert uri == '/api/users?page=2' and headers['Host'] == 'auth.quukk.com'
        assert headers['Authorization'] == 'Bearer fixture.jwt' and not headers['Cookie']
        print('PASS operator gate, exact callback, native cookies, private tenant API, upstream 401, framing headers')


if __name__ == '__main__':
    main()
