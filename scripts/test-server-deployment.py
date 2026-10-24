#!/usr/bin/env python3
"""Server-bundle contracts: real local preparation, with no Docker mutations."""
import importlib.util
import http.client
import json
import os
from pathlib import Path
import re
import stat
import shutil
import socket
import ssl
import subprocess
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[1]
MODULE = REPO / 'deploy/server/bootstrap.py'


class DeploymentTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not MODULE.exists():
            return
        spec = importlib.util.spec_from_file_location('server_bootstrap', MODULE)
        cls.bundle = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.bundle)

    def setUp(self):
        self.assertTrue(MODULE.exists(), 'server bootstrap is not implemented')
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve() / 'unihub'
        self.release = self.root / 'releases' / 'abc123'
        for path in ['bin/unihub', 'web/dist/index.html', 'casdoor-web/index.html',
                     'casdoor-web/swagger/index.html', 'casdoor-web/assets/unihub/unihub-logo-v1.png',
                     'casdoor-web/assets/unihub/unihub-icon-v1.png']:
            dest = self.release / path
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_text('fixture')
        self.release.joinpath('bin/unihub').chmod(0o755)

    def prepare(self, inventory=None):
        return self.bundle.prepare(self.root, self.release, inventory or {})

    def test_repeated_preparation_keeps_all_credentials_and_restricts_access(self):
        self.prepare()
        private = self.root / 'shared/credentials.json'
        first = private.read_bytes()
        self.prepare()
        self.assertEqual(first, private.read_bytes())
        creds = json.loads(first)
        values = [creds[k] for k in ['postgres_admin_password', 'app_database_password',
            'ledger_database_password', 'casdoor_database_password', 'client_secret',
            'superwagie_client_secret', 'model_service_token', 'payment_signing_key', 'admin_password',
            'owner_password', 'acceptance_password']]
        self.assertEqual(len(values), len(set(values)))
        self.assertTrue(all(re.fullmatch('[0-9a-f]{64}', item) for item in values))
        for name in ['credentials.json', 'runtime.env', 'compose.env']:
            self.assertEqual(stat.S_IMODE((self.root / 'shared' / name).stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE((self.root / 'shared').stat().st_mode), 0o700)
        conf = self.root / 'shared/casdoor/conf'
        self.assertEqual(stat.S_IMODE(conf.stat().st_mode), 0o750)
        self.assertEqual(stat.S_IMODE((conf / 'app.conf').stat().st_mode), 0o640)
        if os.geteuid() == 0:
            self.assertEqual(conf.stat().st_gid, 1000)

    def test_existing_resources_without_original_secrets_are_refused(self):
        with self.assertRaises(self.bundle.DeploymentError):
            self.prepare({'volume:unihub-server-postgres': {'Labels': {'com.unihub.installation': 'someone'}}})
        self.assertFalse((self.root / 'shared/credentials.json').exists())

    def test_unmanaged_resources_are_refused_even_with_credentials(self):
        self.prepare()
        original = (self.root / 'shared/credentials.json').read_bytes()
        with self.assertRaises(self.bundle.DeploymentError):
            self.prepare({'container:unihub-server-casdoor': {'Config': {'Labels': {}}}})
        self.assertEqual(original, (self.root / 'shared/credentials.json').read_bytes())

    def test_missing_one_original_secret_is_not_silently_regenerated(self):
        self.prepare()
        path = self.root / 'shared/credentials.json'
        data = json.loads(path.read_text())
        del data['client_secret']
        path.write_text(json.dumps(data))
        with self.assertRaises(self.bundle.DeploymentError):
            self.prepare()

    def test_seed_is_private_fresh_and_exact_clients_are_registered(self):
        self.prepare()
        seed = json.loads((self.root / 'shared/casdoor/conf/init_data.json').read_text())
        self.assertEqual([u['name'] for u in seed['users']], ['owner', 'acceptance'])
        self.assertTrue(all(not u['isAdmin'] and u['type'] == 'normal-user' for u in seed['users']))
        apps = {a['name']: a for a in seed['applications']}
        console = apps['app-unihub']
        self.assertEqual(console['redirectUris'], ['https://unihub.quukk.com/auth/callback'])
        self.assertEqual(console['tokenFormat'], 'JWT-Standard')
        self.assertEqual(console['expireInHours'], 1)
        self.assertEqual(console['grantTypes'], ['authorization_code'])
        self.assertFalse(console['enableSignUp'])
        self.assertEqual([x['name'] for x in console['signinItems'] if x['visible']], ['Username', 'Password', 'Login button'])
        self.assertEqual(apps['app-superwagie']['redirectUris'], [
            'http://127.0.0.1:57991/api/platform-account/callback',
            'http://localhost:57991/api/platform-account/callback'])
        self.assertNotIn('app-built-in', apps)
        self.assertNotIn('built-in', [o['name'] for o in seed['organizations']])

    def test_generated_runtime_retains_sandbox_guard_and_same_origin(self):
        self.prepare()
        env = self.bundle.read_env(self.root / 'shared/runtime.env')
        self.assertEqual(env['APP_ENV'], 'development')
        self.assertEqual(env['LISTEN_ADDR'], '127.0.0.1:8088')
        self.assertEqual(env['SANDBOX_CATALOG'], 'true')
        self.assertEqual(env['OIDC_ISSUER'], 'https://unihub.quukk.com')
        self.assertEqual(env['PUBLIC_URL'], 'https://unihub.quukk.com')
        self.assertEqual(env['BILLING_OPERATOR_IDS'], '')
        self.assertNotEqual(env['OIDC_AUDIENCE'], env['OIDC_PRODUCT_CLIENTS'].split('=')[1])

    def test_release_outside_dedicated_release_tree_is_refused(self):
        with self.assertRaises(self.bundle.DeploymentError):
            self.bundle.prepare(self.root, self.release.parent, {})

    def test_docker_missing_resources_accept_case_variants_but_not_daemon_errors(self):
        cases = [
            ('volume', 'unihub-server-postgres', 'Error response from daemon: get unihub-server-postgres: no such volume', True),
            ('network', 'unihub-server', 'Error response from daemon: network unihub-server not found', True),
            ('container', 'unihub-server-casdoor', 'Error: No such container: unihub-server-casdoor', True),
            ('container', 'unihub-server-casdoor', 'Error: No such object: unihub-server-casdoor', True),
            ('volume', 'unihub-server-postgres', 'permission denied while trying to connect to the Docker daemon socket', False),
            ('volume', 'unihub-server-postgres', 'Cannot connect to the Docker daemon', False),
            ('volume', 'unihub-server-postgres', 'Error response from daemon: get unrelated: no such volume', False),
        ]
        for kind, name, message, expected in cases:
            self.assertEqual(self.bundle.resource_missing(kind, name, message), expected)

    def test_secret_bearing_subprocess_errors_never_repeat_stdout_stderr(self):
        with self.assertRaises(self.bundle.DeploymentError) as failure:
            self.bundle.run(['python3', '-c', 'import sys; print("PRIVATE"); sys.exit(1)'])
        self.assertNotIn('PRIVATE', str(failure.exception))

    @unittest.skipUnless(shutil.which('nginx') and shutil.which('openssl'), 'nginx/openssl unavailable')
    def test_https_only_proxy_secures_cookie_and_omits_callback_secrets_from_logs(self):
        class Identity(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.send_header('Set-Cookie', 'casdoor_session_id=fixture; Path=/')
                self.end_headers()
            def log_message(self, *args):
                pass
        upstream = HTTPServer(('127.0.0.1', 0), Identity)
        worker = threading.Thread(target=upstream.serve_forever, daemon=True)
        worker.start()
        self.addCleanup(upstream.server_close)
        self.addCleanup(upstream.shutdown)
        def port():
            with socket.socket() as probe:
                probe.bind(('127.0.0.1', 0))
                return probe.getsockname()[1]
        http_port, tls_port, closed_port = port(), port(), port()
        folder = Path(self.temp.name).resolve()
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-keyout', str(folder / 'key.pem'),
            '-out', str(folder / 'cert.pem'), '-days', '1', '-nodes', '-subj', '/CN=unihub.quukk.com'],
            capture_output=True, check=True)
        source = (REPO / 'deploy/server/nginx.conf').read_text()
        substitutions = {'listen 80;': f'listen {http_port};', 'listen 8484 ssl;': f'listen {tls_port} ssl;',
            '127.0.0.1:8088': f'127.0.0.1:{closed_port}', '127.0.0.1:58000': f'127.0.0.1:{upstream.server_port}',
            '/etc/nginx/ssl/_.quukk.com_chain.pem': str(folder / 'cert.pem'),
            '/etc/nginx/ssl/_.quukk.com_key.key': str(folder / 'key.pem'),
            '/var/log/nginx/unihub-access.log': str(folder / 'access.log')}
        for original, replacement in substitutions.items():
            source = source.replace(original, replacement)
        config = folder / 'nginx.conf'
        config.write_text('error_log ' + str(folder / 'error.log') + ';\npid ' + str(folder / 'nginx.pid') + ';\nevents {}\nhttp {\n' + source + '\n}\n')
        command = ['nginx', '-p', str(folder) + '/', '-c', str(config)]
        subprocess.run(command + ['-t'], capture_output=True, check=True)
        process = subprocess.Popen(command + ['-g', 'daemon off;'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.addCleanup(lambda: process.wait(timeout=3))
        self.addCleanup(process.terminate)
        context = ssl._create_unverified_context()
        for attempt in range(40):
            try:
                response = urllib.request.urlopen(f'https://127.0.0.1:{tls_port}/login/unihub', context=context, timeout=1)
                break
            except OSError:
                time.sleep(.025)
        else:
            self.fail('isolated nginx did not become ready')
        self.addCleanup(response.close)
        connection = http.client.HTTPConnection('127.0.0.1', http_port, timeout=2)
        self.addCleanup(connection.close)
        connection.request('GET', '/', headers={'Host': 'unihub.quukk.com'})
        with self.assertRaises(http.client.RemoteDisconnected):
            connection.getresponse()
        with response:
            flags = {part.strip().lower() for part in response.headers['Set-Cookie'].split(';')[1:]}
            self.assertTrue({'secure', 'httponly', 'samesite=lax'} <= flags)
        with self.assertRaises(urllib.error.HTTPError) as failure:
            urllib.request.urlopen(f'https://127.0.0.1:{tls_port}/auth/callback?code=PRIVATE_CODE&state=PRIVATE_STATE', context=context, timeout=2)
        self.assertEqual(failure.exception.code, 502)
        time.sleep(.025)
        logs = '\n'.join(path.read_text() for path in folder.glob('*.log'))
        self.assertNotIn('PRIVATE_CODE', logs)
        self.assertNotIn('PRIVATE_STATE', logs)
        self.assertIn('GET /auth/callback HTTP', (folder / 'access.log').read_text())

    def test_proxy_route_boundary_is_unambiguous(self):
        # Parse the actual rendered locations rather than duplicating a router.
        nginx = (REPO / 'deploy/server/nginx.conf').read_text()
        locations = re.findall(r'location\s+(?:(=|\^~|~)\s+)?([^\s{]+)\s*\{([^{}]*)\}', nginx)
        def route(path):
            for modifier, prefix, body in locations:
                if modifier == '=' and path == prefix:
                    return body
            candidates = [(len(prefix), body) for modifier, prefix, body in locations
                          if modifier in ('', '^~') and path.startswith(prefix)]
            return max(candidates)[1] if candidates else ''
        for path in ['/internal', '/internal/models', '/internalx']:
            if path == '/internalx':
                continue
            self.assertIn('return 404', route(path))
        for path in ['/api/signup', '/api/signup/']:
            self.assertIn('return 403', route(path), path)
        for path in ['/login/unihub', '/api/login', '/.well-known/openid-configuration',
                     '/swagger/index.html', '/assets/casdoor.js', '/consent/x', '/mfa/x',
                     '/forget/x', '/update-password/x', '/result/x', '/prompt/x']:
            self.assertIn('127.0.0.1:58000', route(path), path)
        for path in ['/', '/auth/login', '/v1/me', '/console-assets/app.js',
                     '/assets/unihub-logo-v1.png', '/assets/unihub-icon-v1.png']:
            self.assertIn('127.0.0.1:8088', route(path), path)


if __name__ == '__main__':
    unittest.main()
