#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
umask 077

# Python's standard library handles JSON, random credentials, SQL input and
# PKCE without placing secrets in shell arguments or displaying token bodies.
exec python3 - "$@" <<'PY'
import base64
import hashlib
import html
import http.server
import json
import os
from pathlib import Path
import secrets
import shlex
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import webbrowser

root = Path.cwd()
local = root / '.local'
env_path = local / 'dev.env'
credentials_path = local / 'credentials.json'
brand_logo = '/assets/unihub/unihub-logo-v1.png'
brand_icon = '/assets/unihub/unihub-icon-v1.png'
default_casbin_avatar = 'https://cdn.casbin.org/img/casbin.svg'
brand_theme = {'isEnabled': True, 'themeType': 'default', 'colorPrimary': '#087F8C',
    'borderRadius': 10, 'isCompact': False}
organization_branding = {'displayName': 'UniHub', 'logo': brand_logo, 'logoDark': brand_logo,
    'favicon': brand_icon, 'themeData': brand_theme}
application_branding = {**organization_branding, 'title': 'UniHub',
    'footerHtml': '<span>UniHub · 统一身份与账户中心</span>'}
official_homepages = {'http://casdoor.org', 'http://casdoor.org/', 'https://casdoor.org',
    'https://casdoor.org/', 'http://casdoor.com', 'http://casdoor.com/',
    'https://casdoor.com', 'https://casdoor.com/'}
mode = sys.argv[1] if len(sys.argv) == 2 else 'up'
if len(sys.argv) > 2 or mode not in {'up', 'prepare', 'login'}:
    sys.exit('Usage: scripts/dev-setup.sh [up|prepare|login]')

def write_private(path, content, mode=0o600):
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open('x') as stream:
        stream.write(content)
    path.chmod(mode)

def read_env():
    result = {}
    for line in env_path.read_text().splitlines():
        if not line or line.startswith('#'):
            continue
        key, value = line.split('=', 1)
        parts = shlex.split(value)
        result[key] = parts[0] if parts else ''
    return result

def inspect_container(name):
    completed = subprocess.run(['docker', 'inspect', name], capture_output=True, text=True)
    return json.loads(completed.stdout)[0] if completed.returncode == 0 else None

def run(args, *, input=None, quiet=False, env=None):
    completed = subprocess.run(args, input=input, text=True, capture_output=quiet, env=env)
    if completed.returncode:
        # Some database/client errors repeat their input. Do not echo SQL,
        # credentials, Docker environment values or token responses here.
        sys.exit(f'Command failed ({args[0]} {args[1]}). Inspect the local service state; secrets were not printed.')
    return completed.stdout if quiet else ''

def request_json(url, data=None):
    with urllib.request.urlopen(url, data=data, timeout=5) as response:
        return json.load(response)

def wait_json(url, seconds=90):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            return request_json(url)
        except (OSError, ValueError):
            time.sleep(1)
    sys.exit(f'Service did not become ready: {url}')

def mount_source_matches(source, expected):
    if not isinstance(source, str) or not source.startswith('/'):
        return False
    # Docker Desktop on macOS exposes host bind mounts through /host_mnt.
    # Accept only that platform-specific mapping to this exact workspace path.
    if source.startswith('/host_mnt/'):
        if sys.platform != 'darwin':
            return False
        source = source[len('/host_mnt'):]
    return Path(source).resolve() == Path(expected).resolve()

def apply_seed_branding(init):
    # Match only the local product and built-in administration objects. Their
    # identifiers, authentication choices and secrets remain untouched.
    changed = False
    for collection, names, fields in [
        ('organizations', {'unihub', 'built-in'}, organization_branding),
        ('applications', {'app-unihub', 'app-built-in'}, application_branding),
    ]:
        for item in init.get(collection, []):
            if item.get('owner') != 'admin' or item.get('name') not in names:
                continue
            for key, value in fields.items():
                if item.get(key) != value:
                    item[key] = value
                    changed = True
            if collection == 'applications' and item.get('homepageUrl') in official_homepages:
                item['homepageUrl'] = '/login/' + ('built-in' if item['name'] == 'app-built-in' else 'unihub')
                changed = True
            if collection == 'organizations' and (item.get('defaultAvatar') == default_casbin_avatar
                    or (item['name'] == 'unihub' and item.get('defaultAvatar') in (None, ''))):
                item['defaultAvatar'] = brand_icon
                changed = True
    for user in init.get('users', []):
        if (user.get('owner') == 'built-in' and user.get('name') == 'admin'
                and user.get('avatar') == default_casbin_avatar):
            user['avatar'] = brand_icon
            changed = True
    return changed

def sql_literal(value):
    return "'" + value.replace("'", "''") + "'"

def branding_sql():
    # A columns= API request uses xorm MustCols, not a strict column whitelist.
    # Explicit SQL assignments keep every non-branding column unchanged.
    statements = ['BEGIN;']
    for table, names, fields in [
        ('organization', ('unihub', 'built-in'), organization_branding),
        ('application', ('app-unihub', 'app-built-in'), application_branding),
    ]:
        columns = {'displayName': 'display_name', 'logo': 'logo', 'logoDark': 'logo_dark',
            'favicon': 'favicon', 'themeData': 'theme_data', 'title': 'title', 'footerHtml': 'footer_html'}
        assignments, comparisons = [], []
        for key, value in fields.items():
            column = columns[key]
            literal = sql_literal(json.dumps(value, ensure_ascii=False) if key == 'themeData' else value)
            assignments.append(f'{column} = {literal}' + ('::json' if key == 'themeData' else ''))
            comparisons.append(f'{column}::jsonb IS DISTINCT FROM {literal}::jsonb' if key == 'themeData'
                else f'{column} IS DISTINCT FROM {literal}')
        scope = "owner = 'admin' AND name IN (" + ', '.join(sql_literal(name) for name in names) + ')'
        statements.append(f'UPDATE {table} SET ' + ', '.join(assignments) +
            f' WHERE {scope} AND (' + ' OR '.join(comparisons) + ');')
        # Abort the transaction rather than claiming a partially branded setup.
        statements.append(f"DO $$ BEGIN IF (SELECT count(*) FROM {table} WHERE {scope} AND NOT (" +
            ' OR '.join(comparisons) + f")) <> 2 THEN RAISE EXCEPTION 'UniHub {table} branding verification failed'; END IF; END $$;")
    statements.append("UPDATE application SET homepage_url = CASE name WHEN 'app-built-in' THEN '/login/built-in' ELSE '/login/unihub' END "
        "WHERE owner = 'admin' AND name IN ('app-built-in', 'app-unihub') AND homepage_url IN (" +
        ', '.join(sql_literal(url) for url in sorted(official_homepages)) + ');')
    # Replace only upstream defaults. Personal avatars and custom organization
    # defaults are independent user choices and are never rebranded.
    statements.append('UPDATE organization SET default_avatar = ' + sql_literal(brand_icon) +
        " WHERE owner = 'admin' AND (" +
        "(name = 'built-in' AND default_avatar = " + sql_literal(default_casbin_avatar) + ') OR ' +
        "(name = 'unihub' AND (default_avatar IS NULL OR default_avatar IN ('', " +
        sql_literal(default_casbin_avatar) + '))));')
    statements.append('UPDATE "user" SET avatar = ' + sql_literal(brand_icon) +
        " WHERE owner = 'built-in' AND name = 'admin' AND avatar = " + sql_literal(default_casbin_avatar) + ';')
    statements.append('COMMIT;')
    return '\n'.join(statements) + '\n'

if mode == 'login':
    if not env_path.exists() or not credentials_path.exists():
        sys.exit('Run make dev-setup first.')
    env = read_env()
    credentials = json.loads(credentials_path.read_text())
    issuer = env['OIDC_ISSUER']
    if issuer != 'http://localhost:58000' or env['APP_ENV'] != 'development':
        sys.exit('This local login helper only supports the loopback development issuer.')
    discovery = request_json(issuer + '/.well-known/openid-configuration')
    if discovery.get('issuer') != issuer:
        sys.exit('Discovery issuer mismatch.')
    for endpoint in ('authorization_endpoint', 'token_endpoint'):
        parsed = urllib.parse.urlsplit(discovery[endpoint])
        if (parsed.scheme, parsed.netloc) != ('http', 'localhost:58000'):
            sys.exit('Discovery endpoint is not the expected loopback UniHub sign-in server.')
    verifier = secrets.token_urlsafe(32)
    challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b'=').decode()
    state = secrets.token_urlsafe(32)
    redirect = 'http://localhost:8089/callback'
    callback = {}
    try:
        callback_logo = 'data:image/png;base64,' + base64.b64encode((root / 'branding/assets/unihub-logo-v1.png').read_bytes()).decode()
        callback_icon = 'data:image/png;base64,' + base64.b64encode((root / 'branding/assets/unihub-icon-v1.png').read_bytes()).decode()
    except OSError:
        sys.exit('UniHub brand assets are missing. Restore branding/assets before running make dev-login.')

    class Callback(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass  # Authorization codes must not enter console logs.

        def send_brand_page(self, status, title, message):
            # Only caller-owned text enters this page; OAuth query values and
            # token responses are never interpolated into HTML or logs.
            nonce = secrets.token_urlsafe(24)
            page = f'''<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer"><title>UniHub · {html.escape(title)}</title>
<link rel="icon" href="{callback_icon}">
<style nonce="{nonce}">
*{{box-sizing:border-box}}body{{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:#F4F8F8;color:#163137;font:16px/1.65 system-ui,sans-serif}}
main{{width:min(100%,520px);padding:48px 36px;background:#fff;border:1px solid #DCE8E8;border-radius:20px;box-shadow:0 20px 60px #123A4210;text-align:center}}
img{{width:240px;max-width:80%;height:auto;margin-bottom:24px}}h1{{font-size:24px;margin:0 0 16px;color:#087F8C}}p{{margin:0;color:#52666B}}small{{display:block;margin-top:28px;color:#718489}}
@media(max-width:480px){{main{{padding:36px 24px}}h1{{font-size:22px}}}}
</style><script nonce="{nonce}">history.replaceState(null, '', '/callback');</script></head>
<body><main><img src="{callback_logo}" alt="UniHub"><h1>{html.escape(title)}</h1><p>{html.escape(message)}</p><small>UniHub · 统一身份与账户中心</small></main></body></html>'''.encode('utf-8')
            self.send_response(status)
            self.send_header('Content-Type', 'text/html; charset=utf-8')
            self.send_header('Content-Length', str(len(page)))
            self.send_header('Cache-Control', 'no-store')
            self.send_header('Referrer-Policy', 'no-referrer')
            self.send_header('X-Content-Type-Options', 'nosniff')
            self.send_header('Content-Security-Policy', f"default-src 'none'; img-src data:; style-src 'nonce-{nonce}'; script-src 'nonce-{nonce}'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
            self.end_headers()
            self.wfile.write(page)

        def do_GET(self):
            parsed = urllib.parse.urlsplit(self.path)
            values = urllib.parse.parse_qs(parsed.query)
            if parsed.path != '/callback':
                self.send_brand_page(404, '页面不存在', '请使用终端打开的 UniHub 登录链接，并在登录完成后返回终端。')
                return
            if values.get('state') != [state] or values.get('iss') != [issuer]:
                self.send_brand_page(400, '无法验证登录响应', '请返回终端重新发起登录。本次响应未被接受，也未保存访问令牌。')
                return
            if values.get('error') or len(values.get('code', [])) != 1:
                callback['error'] = True
                self.send_brand_page(400, '授权未完成', '请返回终端查看登录结果，并重新发起登录。本次未保存访问令牌。')
                return
            callback['code'] = values['code'][0]
            self.send_brand_page(200, '授权已收到', '请返回终端。终端将继续交换令牌并验证 UniHub API；本页不代表验证已通过。')

    server = http.server.HTTPServer(('127.0.0.1', 8089), Callback)
    server.timeout = 1
    query = urllib.parse.urlencode({'client_id': env['OIDC_AUDIENCE'], 'redirect_uri': redirect,
        'response_type': 'code', 'scope': 'openid profile email', 'state': state,
        'code_challenge': challenge, 'code_challenge_method': 'S256'})
    login_url = discovery['authorization_endpoint'] + '?' + query
    print('Opening UniHub sign-in. Sign in as alice or bob using .local/credentials.json.', flush=True)
    if not webbrowser.open(login_url):
        print('Open this authorization URL in your local browser:', login_url, flush=True)
    deadline = time.monotonic() + 300
    while not callback and time.monotonic() < deadline:
        server.handle_request()
    server.server_close()
    if 'code' not in callback:
        sys.exit('Login canceled or timed out; no token was saved.')
    body = urllib.parse.urlencode({'grant_type': 'authorization_code', 'client_id': env['OIDC_AUDIENCE'],
        'client_secret': credentials['client_secret'], 'redirect_uri': redirect,
        'code': callback['code'], 'code_verifier': verifier}).encode()
    try:
        token = request_json(discovery['token_endpoint'], body)
    except (OSError, ValueError):
        sys.exit('Token exchange failed. The code is single-use; run make dev-login again.')
    access = token.get('access_token')
    if not isinstance(access, str) or not access or token.get('token_type', '').lower() != 'bearer':
        sys.exit('Token endpoint did not return a bearer access token.')
    # The API performs actual signature and claim verification; this helper
    # only retrieves a token through the real browser code+PKCE flow.
    request = urllib.request.Request('http://127.0.0.1:8088/v1/me', headers={'Authorization': 'Bearer ' + access})
    try:
        with urllib.request.urlopen(request, timeout=5) as response:
            response.read()
        accepted = True
    except urllib.error.HTTPError:
        sys.exit('The running API rejected the access token; no token was saved.')
    except OSError:
        accepted = False
    token_path = local / 'access-token'
    token_path.write_text(access + '\n')
    token_path.chmod(0o600)
    print('Access token saved privately to .local/access-token. ' + ('UniHub /v1/me accepted it.' if accepted else 'API was unavailable; API acceptance remains unverified.'))
    sys.exit(0)

if subprocess.run(['docker', 'info'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
    sys.exit('Docker is unavailable. Start Docker Desktop and retry.')
local.mkdir(mode=0o700, exist_ok=True)
local.chmod(0o700)
if not env_path.exists():
    existing = inspect_container('unihub-dev-postgres')
    volume_exists = subprocess.run(['docker', 'volume', 'inspect', 'unihub-dev-postgres'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
    if volume_exists and not existing:
        sys.exit('Existing PostgreSQL volume has no container/config to recover credentials from. Restore .local/dev.env before continuing; data was not changed.')
    inherited = dict(item.split('=', 1) for item in existing['Config']['Env'] if '=' in item) if existing else {}
    admin = inherited.get('POSTGRES_USER', 'unihub')
    password = inherited.get('POSTGRES_PASSWORD') or secrets.token_hex(32)
    if not admin.replace('_', '').isalnum():
        sys.exit('Existing PostgreSQL role requires manual configuration.')
    user_url = urllib.parse.quote(admin, safe='')
    pass_url = urllib.parse.quote(password, safe='')
    env = {'APP_ENV': 'development', 'LISTEN_ADDR': '127.0.0.1:8088',
        'DATABASE_URL': f'postgresql://{user_url}:{pass_url}@127.0.0.1:55438/unihub?sslmode=disable',
        'LEDGER_URL': 'http://127.0.0.1:53068', 'LEDGER_NAME': 'unihub',
        'OIDC_ISSUER': 'http://localhost:58000', 'OIDC_AUDIENCE': 'unihub-local',
        'PAYMENT_MODE': 'sandbox', 'PAYMENT_MERCHANT': 'unihub-sandbox', 'PAYMENT_SIGNING_KEY': secrets.token_hex(32),
        'POSTGRES_ADMIN_USER': admin, 'POSTGRES_ADMIN_PASSWORD': password,
        'LEDGER_POSTGRES_URI': f'postgresql://{user_url}:{pass_url}@unihub-dev-postgres:5432/ledger?sslmode=disable',
        'CASDOOR_DATABASE_PASSWORD': secrets.token_hex(32), 'LOCAL_GID': str(os.getgid())}
    write_private(env_path, '# Generated local development values. Never commit this file.\n' + ''.join(f'{key}={shlex.quote(value)}\n' for key, value in env.items()))
env = read_env()
if env.get('APP_ENV') != 'development':
    sys.exit('dev-setup refuses non-development configuration.')
if not credentials_path.exists():
    if inspect_container('unihub-dev-casdoor'):
        sys.exit('Existing Casdoor requires its original .local/credentials.json. Restore it; credentials were not rotated.')
    credentials = {'organization': 'unihub', 'application': 'app-unihub', 'client_id': 'unihub-local',
        'client_secret': secrets.token_hex(32), 'admin': {'organization': 'built-in', 'username': 'admin', 'password': secrets.token_urlsafe(24)},
        'users': [{'username': name, 'password': secrets.token_urlsafe(24)} for name in ['alice', 'bob']]}
    write_private(credentials_path, json.dumps(credentials, indent=2) + '\n')
credentials = json.loads(credentials_path.read_text())
conf_dir = local / 'casdoor' / 'conf'
conf_dir.mkdir(parents=True, exist_ok=True)
conf_dir.chmod(0o750)
app_conf = conf_dir / 'app.conf'
if not app_conf.exists():
    config = f'''appname = casdoor
httpport = 8000
runmode = prod
copyrequestbody = true
driverName = postgres
dataSourceName = user=casdoor password={env['CASDOOR_DATABASE_PASSWORD']} host=unihub-dev-postgres port=5432 sslmode=disable dbname=casdoor
dbName = casdoor
showSql = false
origin = http://localhost:58000
originFrontend = http://localhost:58000
initAdminPassword = {credentials['admin']['password']}
initDataFile = /conf/init_data.json
initDataNewOnly = true
isDemoMode = false
defaultApplication = app-built-in
defaultLanguage = en
staticBaseUrl = https://cdn.casbin.org
logConfig = {{"adapter":"file","filename":"logs/casdoor.log","maxdays":7,"perm":"0600"}}
'''
    write_private(app_conf, config, 0o640)
init_path = conf_dir / 'init_data.json'
if not init_path.exists():
    init = {
        'organizations': [{'owner': 'admin', 'name': 'unihub', **organization_branding, 'defaultAvatar': brand_icon,
            'passwordType': 'bcrypt', 'passwordOptions': ['AtLeast8'], 'defaultApplication': 'app-unihub',
            'defaultTokenFormat': 'JWT-Standard', 'languages': ['en', 'zh'], 'countryCodes': ['CN'],
            'accountItems': [{'name': 'Name', 'visible': True, 'viewRule': 'Self', 'modifyRule': 'Immutable'},
                {'name': 'Display name', 'visible': True, 'viewRule': 'Self', 'modifyRule': 'Self'}]}],
        'applications': [{'owner': 'admin', 'name': 'app-unihub', **application_branding,
            'organization': 'unihub', 'cert': 'cert-built-in', 'category': 'Default', 'type': 'All',
            'enablePassword': True, 'enableSignUp': False, 'disableSignin': False, 'isShared': False,
            'clientId': credentials['client_id'], 'clientSecret': credentials['client_secret'],
            'providers': [], 'signinMethods': [{'name': 'Password', 'displayName': 'Password', 'rule': 'All'}],
            'signinItems': [{'name': 'Username', 'visible': True, 'rule': 'None'},
                {'name': 'Password', 'visible': True, 'rule': 'None'},
                {'name': 'Login button', 'visible': True, 'rule': 'None'}],
            'grantTypes': ['authorization_code'], 'redirectUris': ['http://localhost:8089/callback'],
            'tokenFormat': 'JWT-Standard', 'expireInHours': 1, 'cookieExpireInHours': 8,
            'failedSigninLimit': 5, 'failedSigninFrozenTime': 15}],
        'users': [{'owner': 'unihub', 'name': u['username'], 'id': secrets.token_hex(16), 'type': 'normal-user',
            'password': u['password'], 'displayName': u['username'].title(), 'email': u['username'] + '@example.test',
            'isAdmin': False, 'isForbidden': False, 'isDeleted': False, 'signupApplication': 'app-unihub'}
            for u in credentials['users']]}
    write_private(init_path, json.dumps(init, indent=2) + '\n', 0o640)
# Upgrade existing seed branding as well as the known original button defect.
# initDataNewOnly preserves live objects, so narrow live-data updates follow.
# Casdoor creates built-in objects before reading the seed; do not create
# incomplete copies of their authentication configuration here.
init = json.loads(init_path.read_text())
seed_changed = apply_seed_branding(init)
for application in init.get('applications', []):
    if application.get('owner') != 'admin' or application.get('name') != 'app-unihub':
        continue
    items = application.get('signinItems', [])
    if items and not any(item.get('name') == 'Login button' for item in items):
        items.append({'name': 'Login button', 'visible': True, 'rule': 'None'})
        seed_changed = True
if seed_changed:
    init_path.write_text(json.dumps(init, indent=2) + '\n')
    init_path.chmod(0o640)
print('Local configuration prepared; credentials are in .local/credentials.json (not printed).', flush=True)
if mode == 'prepare':
    sys.exit(0)

process_env = {**os.environ, **env}
compose = ['docker', 'compose', '--env-file', str(env_path)]
run(compose + ['config', '--quiet'], env=process_env)
for resource, name in [('network', 'unihub-dev'), ('volume', 'unihub-dev-postgres')]:
    if subprocess.run(['docker', resource, 'inspect', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode:
        run(['docker', resource, 'create', name], quiet=True)

def start_service(service):
    name = 'unihub-dev-' + service
    existing = inspect_container(name)
    if existing:
        expected = {'postgres': 'postgres:', 'ledger': 'ghcr.io/formancehq/ledger:v2.4.16', 'casdoor': 'casbin/casdoor:v4.23.0'}[service]
        if not existing['Config']['Image'].startswith(expected):
            sys.exit(f'{name} has an unexpected image. Resolve it explicitly; no container was replaced.')
        expected_port = {'postgres': ('5432/tcp', '55438'), 'ledger': ('3068/tcp', '53068'), 'casdoor': ('8000/tcp', '58000')}[service]
        bindings = existing['HostConfig'].get('PortBindings', {}).get(expected_port[0], [])
        if not bindings or any(binding['HostIp'] != '127.0.0.1' or binding['HostPort'] != expected_port[1] for binding in bindings):
            sys.exit(f'{name} must publish only its documented loopback port. Resolve it explicitly; no container was replaced.')
        if service == 'casdoor':
            mounts = {mount['Destination']: mount for mount in existing.get('Mounts', [])}
            web_mount = mounts.get('/web/build', {})
            expected_web = str((local / 'casdoor' / 'web').resolve())
            if web_mount.get('Type') != 'bind' or not mount_source_matches(web_mount.get('Source'), expected_web) or web_mount.get('RW') is not False:
                labels = existing['Config'].get('Labels') or {}
                conf_mount = mounts.get('/conf', {})
                expected_conf = str(conf_dir.resolve())
                if (labels.get('com.docker.compose.project') != 'unihub'
                        or labels.get('com.docker.compose.service') != 'casdoor'
                        or conf_mount.get('Type') != 'bind'
                        or not mount_source_matches(conf_mount.get('Source'), expected_conf)
                        or conf_mount.get('RW') is not False):
                    sys.exit(f'{name} is not this workspace\'s Compose-managed sign-in service. Resolve the container conflict explicitly; no container was replaced.')
                # Only the stateless sign-in container needs the new readonly
                # frontend mount. Its database, config and log volume persist.
                run(compose + ['up', '-d', '--no-deps', '--force-recreate', 'casdoor'], env=process_env)
                print('Updated the UniHub sign-in frontend mount; PostgreSQL and ledger containers were preserved.', flush=True)
                return
        if not existing['State']['Running']:
            run(['docker', 'start', name], quiet=True)
        if 'unihub-dev' not in existing['NetworkSettings']['Networks']:
            run(['docker', 'network', 'connect', 'unihub-dev', name], quiet=True)
        print(f'Reusing {name}; no container was stopped or removed.', flush=True)
    else:
        run(compose + ['up', '-d', '--no-deps', service], env=process_env)

start_service('postgres')
for attempt in range(60):
    if subprocess.run(['docker', 'exec', 'unihub-dev-postgres', 'pg_isready', '-U', env['POSTGRES_ADMIN_USER']], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
        break
    time.sleep(1)
else:
    sys.exit('PostgreSQL did not become ready.')
admin_sql = '"' + env['POSTGRES_ADMIN_USER'].replace('"', '""') + '"'
casdoor_password = env['CASDOOR_DATABASE_PASSWORD'].replace("'", "''")
sql = f'''DO $$ BEGIN
IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'casdoor') THEN
  CREATE ROLE casdoor LOGIN PASSWORD '{casdoor_password}';
END IF;
END $$;
SELECT 'CREATE DATABASE unihub OWNER {admin_sql}' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'unihub')\\gexec
SELECT 'CREATE DATABASE ledger OWNER {admin_sql}' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'ledger')\\gexec
SELECT 'CREATE DATABASE casdoor OWNER casdoor' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'casdoor')\\gexec
'''
run(['docker', 'exec', '-i', 'unihub-dev-postgres', 'psql', '-U', env['POSTGRES_ADMIN_USER'], '-d', 'postgres', '-v', 'ON_ERROR_STOP=1', '-q'], input=sql, quiet=True)
start_service('ledger')
# The helper verifies the pinned upstream bundle and publishes files in place,
# preserving an existing bind mount's directory inode. Prepare mode exits above.
branding_result = json.loads(run(['python3', 'scripts/prepare-branding.py', '--json'], quiet=True))
if branding_result.get('output') != str((local / 'casdoor' / 'web').resolve()):
    sys.exit('UniHub frontend preparation returned an unexpected directory; the sign-in container was not changed.')
start_service('casdoor')
info = wait_json(env['LEDGER_URL'] + '/_info')
discovery = wait_json(env['OIDC_ISSUER'] + '/.well-known/openid-configuration')
if discovery.get('issuer') != env['OIDC_ISSUER']:
    sys.exit('Casdoor discovery issuer differs from OIDC_ISSUER.')
# The first seed supplied Username/Password but omitted Casdoor's explicit
# Login button row. Append it only when missing; preserve every other app
# field, credential, user, key and any existing explicit visibility choice.
repair_sql = '''UPDATE application
SET signin_items = (signin_items::jsonb || '[{"name":"Login button","visible":true,"rule":"None"}]'::jsonb)::text
WHERE owner='admin' AND name='app-unihub'
  AND jsonb_typeof(signin_items::jsonb)='array'
  AND jsonb_array_length(signin_items::jsonb)>0
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(signin_items::jsonb) AS item
    WHERE item->>'name'='Login button'
  );
'''
run(['docker', 'exec', '-i', 'unihub-dev-postgres', 'psql', '-U', env['POSTGRES_ADMIN_USER'], '-d', 'casdoor', '-v', 'ON_ERROR_STOP=1', '-q'], input=repair_sql, quiet=True)
run(['docker', 'exec', '-i', 'unihub-dev-postgres', 'psql', '-U', env['POSTGRES_ADMIN_USER'], '-d', 'casdoor', '-v', 'ON_ERROR_STOP=1', '-q'], input=branding_sql(), quiet=True)
print('Ready: PostgreSQL :55438; Formance :53068; UniHub sign-in http://localhost:58000.', flush=True)
print('Next: make migrate; make api (includes recovery worker); then make dev-login in another terminal.', flush=True)
PY
