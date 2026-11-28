// Run only against the disposable two-listener staging database described in README.
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const http = require('node:http');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const { firefox } = require('playwright');

// Pin the original UI's public hostname to the local HTTPS fixture, preserving TLS name checks.
async function startPublicFixtureProxy() {
  const server = http.createServer((_req, res) => res.writeHead(403).end());
  server.on('connect', (req, client, head) => {
    if (req.url !== 'auth.quukk.com:443') {
      client.destroy();
      return;
    }
    const upstream = net.createConnection(49444, '127.0.0.1', () => {
      client.write('HTTP/1.1 200 Connection established\r\n\r\n');
      upstream.write(head);
      client.pipe(upstream).pipe(client);
    });
    client.on('error', () => upstream.destroy());
    upstream.on('error', () => client.destroy());
    client.on('close', () => upstream.destroy());
    upstream.on('close', () => client.destroy());
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  server.unref();
  return server;
}

async function run() {
  const legacy = process.argv.includes('--legacy');
  const invalidPkce = process.argv.includes('--invalid-pkce');
  const invalidSignature = process.argv.includes('--invalid-signature');
  const invalidIssuer = process.argv.includes('--invalid-issuer');
  const crossIssuer = process.argv.includes('--cross-issuer');
  const checkProfile = process.argv.includes('--profile');
  const origin = legacy ? 'http://localhost:13002' : 'https://localhost:49443';
  const prefix = legacy ? '' : '/auth';
  const issuer = origin + prefix + '/oidc';
  const callback = origin + prefix + '/console/callback';
  const userApi = (legacy ? 'https://auth.quukk.com' : origin + prefix + '/management') + '/api/users';
  const profile = fs.mkdtempSync(path.join(process.env.STAGE_TMP || os.tmpdir(), 'logto-browser-'));
  const profileSource = process.env.STAGE_FIREFOX_TRUST;
  for (const name of ['cert9.db', 'key4.db']) {
    fs.copyFileSync(path.join(profileSource, name), path.join(profile, name));
  }
  const publicProxy = legacy && await startPublicFixtureProxy();
  const context = await firefox.launchPersistentContext(profile, {
    headless: true,
    locale: 'en-US',
    // This loopback-only CONNECT proxy maps auth.quukk.com:443 to the fixture TLS
    // listener. The real public service must never see a fixture token.
    ...(publicProxy && { proxy: { server: 'http://127.0.0.1:' + publicProxy.address().port, bypass: 'localhost,127.0.0.1' } }),
  });
  try {
    if (!legacy) {
      await context.addCookies([{ url: origin, name: 'unihub_session', value: 'stage-operator', secure: true, httpOnly: true }]);
    }
    const page = await context.newPage();
    const escaped = [];
    let auth;
    let listAuthorization;
    let checkedRefresh = false;
    let modifiedSignature = false;
    let invalidGrant = false;
    let discoverySeen = false;
    let callbackState;
    let challengeMatches = false;
    page.on('request', request => {
      const target = new URL(request.url());
      if (target.origin + target.pathname === callback) callbackState = target.searchParams.get('state');
    });
    await context.route('**/*', async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      // Stock branding and optional product telemetry are unrelated to Console routing.
      if (url.href === 'https://logto.io/logo.svg' || url.href === 'https://numbers.logto.io/pull.json') {
        return route.abort();
      }
      if (!(legacy && url.origin === 'https://auth.quukk.com') &&
          (url.origin !== origin || (prefix && !url.pathname.startsWith(prefix + '/')))) {
        escaped.push(url.origin + url.pathname);
        return route.abort();
      }
      if ((crossIssuer || invalidSignature) && url.origin + url.pathname === userApi && request.method() === 'GET') {
        listAuthorization = await request.headerValue('authorization');
      }
      if (invalidIssuer && url.href === issuer + '/.well-known/openid-configuration') {
        const real = await route.fetch();
        const metadata = await real.json();
        discoverySeen = true;
        return route.fulfill({ response: real, json: { ...metadata, issuer: 'https://invalid.example/oidc' } });
      }
      if (url.href.split('?')[0] === issuer + '/auth') auth = url;
      if (url.href.split('?')[0] === callback) callbackState = url.searchParams.get('state');
      if (url.href === issuer + '/token' && request.method() === 'POST') {
        const body = new URLSearchParams(request.postData());
        if (body.get('grant_type') === 'authorization_code' && body.get('client_id') === 'admin-console') {
          challengeMatches = crypto.createHash('sha256').update(body.get('code_verifier')).digest('base64url') === auth?.searchParams.get('code_challenge');
          if (invalidPkce) {
            body.set('code_verifier', crypto.randomBytes(48).toString('base64url'));
            return route.continue({ postData: body.toString() });
          }
          if (crossIssuer || invalidSignature) {
            const issued = await route.fetch();
            assert(issued.ok(), 'fixture token exchange failed');
            const tokens = await issued.json();
            if (invalidSignature) {
              const parts = tokens.id_token.split('.');
              const signature = Buffer.from(parts[2], 'base64url');
              signature[0] ^= 1;
              parts[2] = signature.toString('base64url');
              modifiedSignature = true;
              return route.fulfill({ response: issued, json: { ...tokens, id_token: parts.join('.') } });
            }
            // Test a fresh fixture refresh token before the SPA can use or rotate it.
            const otherIssuer = legacy ? 'https://localhost:49443/auth/oidc' : 'http://localhost:13002/oidc';
            assert(typeof tokens.refresh_token === 'string' && tokens.refresh_token.length > 0);
            const refreshed = await fetch(otherIssuer + '/token', {
              method: 'POST',
              headers: legacy ? { Cookie: 'unihub_session=stage-operator' } : {},
              redirect: 'error',
              body: new URLSearchParams({
                grant_type: 'refresh_token', client_id: 'admin-console', refresh_token: tokens.refresh_token,
              }),
            });
            assert.equal(refreshed.status, 400);
            assert.equal((await refreshed.json()).error, 'invalid_grant');
            checkedRefresh = true;
            return route.fulfill({ response: issued });
          }
        }
      }
      return route.continue();
    });
    const tokenResponse = page.waitForResponse((r) => r.url() === issuer + '/token' && r.request().method() === 'POST', { timeout: 45000 });
    tokenResponse.catch(() => {});
    await page.goto(origin + prefix + '/console/users');
    if (invalidIssuer) {
      await page.getByText(/unexpected portal issuer/i).first().waitFor({timeout:10000});
      assert(discoverySeen && !auth && !callbackState);
      console.log('PASS mismatched issuer rejected before authorization');
      return;
    }
    await page.waitForURL((u) => u.pathname === prefix + '/sign-in');
    assert.equal(auth.searchParams.get('redirect_uri'), callback);
    assert.equal(auth.searchParams.get('code_challenge_method'), 'S256');
    assert(auth.searchParams.get('state'));
    await page.locator('input:not([type=hidden])').first().fill('stageoperator');
    const password = page.locator('input[type=password]');
    if (!await password.count()) await page.locator('input:not([type=hidden])').first().press('Enter');
    await password.fill(fs.readFileSync(process.env.STAGE_PASSWORD_FILE, 'utf8').trim());
    await password.press('Enter');
    const response = await tokenResponse;
    assert(challengeMatches);
    assert(callbackState && callbackState === auth.searchParams.get('state'), 'callback state must match');
    if (invalidPkce) {
      invalidGrant = response.status() === 400 && (await response.json()).error === 'invalid_grant';
      assert(invalidGrant);
      console.log('PASS native token endpoint rejected mismatched verifier');
      return;
    }
    assert(response.ok());
    if (invalidSignature) {
      await page.getByText(/signature verification failed/i).first().waitFor({timeout:10000});
      assert(modifiedSignature);
      assert(!listAuthorization);
      console.log('PASS SDK rejected invalid ID-token signature at callback');
      return;
    }
    const row = page.getByText('listfixture', { exact: true });
    await row.waitFor({timeout:30000});
    assert.equal(new URL(page.url()).pathname, prefix + '/console/users');
    await row.click();
    const name = page.locator('input[name=name]');
    await name.waitFor();
    const oldName = await name.inputValue();
    const newName = 'Fixture ' + crypto.randomBytes(5).toString('hex');
    async function save(value) {
      await name.fill(value);
      const updated = page.waitForResponse(r => r.url() === userApi + '/stage-listed-user' && r.request().method() === 'PATCH');
      await page.getByRole('button', { name: /^save(?: changes)?$/i }).click();
      assert((await updated).ok());
    }
    try {
      await save(newName);
      await page.reload();
      await page.waitForFunction(v => document.querySelector('input[name=name]')?.value === v, newName);
    } finally {
      await save(oldName);
    }
    if (crossIssuer) {
      // Only fixed loopback test destinations receive disposable fixture credentials.
      assert(checkedRefresh);
      const otherApi = legacy ? 'https://localhost:49443/auth/management/api/users' : 'http://localhost:13001/api/users';
      const headers = legacy ? { Cookie: 'unihub_session=stage-operator' } : {};
      assert(listAuthorization?.startsWith('Bearer '));
      const rejected = await fetch(otherApi, {
        headers: { ...headers, Authorization: listAuthorization },
        redirect: 'error',
      });
      assert.equal(rejected.status, 401);
      console.log('PASS other admin issuer rejects refresh token and other Management API rejects user JWT');
    }
    if (checkProfile) {
      const opened = context.waitForEvent('page');
      opened.catch(() => {});
      const profileResponse = new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error('Profile API timeout')), 30000);
        const observe = r => {
          if (r.url() === origin + prefix + '/api/my-account' && r.ok()) {
            clearTimeout(timer);
            context.off('response', observe);
            resolve();
          }
        };
        context.on('response', observe);
      });
      profileResponse.catch(() => {});
      await page.locator('div[class*="__topbar"] div[role="button"]').last().click();
      await page.getByRole('menuitem', { name: 'Profile', exact: true }).click();
      const profilePage = await opened;
      await profileResponse;
      await profilePage.waitForURL(u => u.pathname === prefix + '/account/profile');
      console.log('PASS profile menu opens Account Center, completes its native login and reads account data');
    }
    assert.deepEqual(escaped, []);
    console.log('PASS ' + (legacy ? 'original' : 'prefixed') + ' Console: native login, state, S256, users list, edit, reload, restore');
  } finally {
    await context.close();
    if (publicProxy) publicProxy.close();
    fs.rmSync(profile, { recursive: true, force: true });
  }
}
run().catch(error => {
  console.error(String(error).replace(/\?[^\s]+/g, '?[redacted]'));
  process.exitCode = 1;
});
