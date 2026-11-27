import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync, randomBytes, sign } from 'node:crypto';
import { createServer } from 'node:http';
import { test } from 'node:test';
import pg from 'pg';
import { buildApp } from '../src/app.js';
import { readConfig } from '../src/config.js';

async function testDatabase(t) {
  const connectionString = process.env.UNIHUB_TEST_DATABASE_URL;
  assert.ok(connectionString, 'set UNIHUB_TEST_DATABASE_URL to a dedicated PostgreSQL test database');
  const control = new pg.Pool({ connectionString });
  const schema = `unihub_test_${randomBytes(8).toString('hex')}`;
  await control.query(`CREATE SCHEMA ${schema}`);
  const pool = new pg.Pool({ connectionString, options: `-c search_path=${schema}` });
  t.after(async () => {
    await pool.end();
    try { await control.query(`DROP SCHEMA ${schema} CASCADE`); }
    finally { await control.end(); }
  });
  return pool;
}

async function fixture(t) {
  const pool = await testDatabase(t);
  const clientId = 'unihub-test';
  const { privateKey, publicKey } = generateKeyPairSync('ec', { namedCurve: 'secp384r1' });
  let attempt;
  let exchanges = 0;
  let issuer;
  let subject = 'logto-user';
  const provider = createServer(async (request, response) => {
    function send(status, value) {
      response.writeHead(status, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify(value));
    }

    if (request.url === '/oidc/.well-known/openid-configuration') {
      return send(200, {
        issuer, authorization_endpoint: `${issuer}/auth`, token_endpoint: `${issuer}/token`,
        end_session_endpoint: `${issuer}/session/end`, jwks_uri: `${issuer}/jwks`,
        id_token_signing_alg_values_supported: ['ES384'], code_challenge_methods_supported: ['S256'],
      });
    }
    if (request.url === '/oidc/jwks') {
      return send(200, { keys: [{ ...publicKey.export({ format: 'jwk' }), kid: 'fixture', alg: 'ES384' }] });
    }
    if (request.url === '/oidc/token' && request.method === 'POST') {
      exchanges++;
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const body = new URLSearchParams(Buffer.concat(chunks).toString());
      const challenge = createHash('sha256').update(body.get('code_verifier') ?? '').digest('base64url');
      if (body.get('code') !== 'valid-code' || challenge !== attempt?.searchParams.get('code_challenge')) {
        return send(400, { error: 'invalid_grant' });
      }
      const now = Math.floor(Date.now() / 1000);
      const payload = {
        iss: issuer, aud: clientId, sub: subject, iat: now, exp: now + 600,
        nonce: attempt.searchParams.get('nonce'), name: '<成员>', email: 'same@example.test',
      };
      const header = { alg: 'ES384', kid: 'fixture' };
      const parts = [header, payload].map((item) => Buffer.from(JSON.stringify(item)).toString('base64url'));
      const unsigned = parts.join('.');
      const signature = sign('sha384', Buffer.from(unsigned), { key: privateKey, dsaEncoding: 'ieee-p1363' });
      return send(200, { token_type: 'Bearer', access_token: 'fixture-token', id_token: `${unsigned}.${signature.toString('base64url')}` });
    }
    return send(404, { error: 'not_found' });
  });
  await new Promise((resolve) => provider.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => provider.close(resolve)));
  issuer = `http://127.0.0.1:${provider.address().port}/oidc`;
  const config = {
    origin: new URL('http://127.0.0.1:14000'), issuer: new URL(issuer), clientId,
    clientSecret: 'fixture-client-secret', sessionKey: randomBytes(32),
    pool, region: 'cn', currency: 'CNY', operatorSubjects: new Set(['operator']),
  };
  const app = await buildApp(config);
  t.after(() => app.close());
  return {
    app, issuer, config, pool,
    get exchangeCount() { return exchanges; },
    async login(user = 'logto-user', loginUrl = '/login') {
      subject = user;
      const result = await app.inject(loginUrl);
      assert.equal(result.statusCode, 302);
      attempt = new URL(result.headers.location);
      assert.equal(attempt.pathname, '/oidc/auth');
      assert.equal(attempt.searchParams.get('code_challenge_method'), 'S256');
      assert.ok(attempt.searchParams.get('nonce'));
      const state = attempt.searchParams.get('state');
      const cookie = result.cookies[0];
      return { callback: `/auth/callback?code=valid-code&state=${encodeURIComponent(state)}`, cookies: { [cookie.name]: cookie.value } };
    },
  };
}

async function signIn(context, subject) {
  const attempt = await context.login(subject);
  const result = await context.app.inject({ url: attempt.callback, cookies: attempt.cookies });
  assert.equal(result.headers.location, '/');
  const cookie = result.cookies[0];
  const cookies = { [cookie.name]: cookie.value };
  const state = await context.app.inject({ url: '/api/session', cookies });
  assert.equal(state.statusCode, 200);
  return { cookies, ...state.json() };
}

function change(app, session, method, url, payload) {
  return app.inject({ method, url, cookies: session.cookies, payload, headers: { 'x-csrf-token': session.csrf } });
}

test('门户用 OIDC 授权码建立会话，校验退出并清除会话', async (t) => {
  const { app, login, issuer } = await fixture(t);
  const homeBefore = await app.inject('/');
  assert.match(homeBefore.body, /登录 UniHub/);
  assert.equal((await app.inject('/auth/callback?code=valid-code')).statusCode, 303);
  const attempt = await login();
  const callback = await app.inject({ url: attempt.callback, cookies: attempt.cookies });
  assert.equal(callback.statusCode, 303);
  assert.equal(callback.headers.location, '/');
  const cookie = callback.cookies[0];
  assert.equal(cookie.httpOnly, true);
  const cookies = { [cookie.name]: cookie.value };
  const home = await app.inject({ url: '/', cookies });
  assert.match(home.body, /&lt;成员&gt;/);
  const token = home.body.match(/name="csrf" value="([^"]+)"/)[1];
  assert.equal((await app.inject({ method: 'POST', url: '/logout', cookies })).statusCode, 403);
  const logout = await app.inject({
    method: 'POST', url: '/logout', cookies,
    headers: { 'content-type': 'application/x-www-form-urlencoded' },
    payload: new URLSearchParams({ csrf: token }).toString(),
  });
  assert.equal(logout.statusCode, 303);
  assert.ok(logout.headers.location.startsWith(`${issuer}/session/end?`));
  assert.equal(logout.cookies[0].value, '');
  assert.equal((await app.inject({ url: '/api/session', cookies })).statusCode, 401);
});

test('回调的登录状态不匹配时不向认证服务兑换令牌', async (t) => {
  const context = await fixture(t);
  const attempt = await context.login();
  const result = await context.app.inject({ url: '/auth/callback?code=valid-code&state=wrong', cookies: attempt.cookies });
  assert.equal(result.statusCode, 303);
  assert.equal(result.headers.location, '/?login=failed');
  assert.equal((await context.app.inject({
    url: `${attempt.callback}&state=extra`, cookies: attempt.cookies,
  })).headers.location, '/?login=failed');
  assert.equal(context.exchangeCount, 0);
});

test('区域配置只接受 CN/CNY 或 US/USD，迁移可重复运行并拒绝重用其他区账库', async (t) => {
  const env = {
    UNIHUB_ORIGIN: 'http://127.0.0.1:14000', LOGTO_ISSUER: 'http://localhost:13001/oidc',
    LOGTO_CLIENT_ID: 'test', LOGTO_CLIENT_SECRET: 'fixture', UNIHUB_SESSION_KEY: randomBytes(32).toString('base64url'),
    DATABASE_URL: 'postgresql://localhost/unihub', UNIHUB_REGION: 'cn', UNIHUB_CURRENCY: 'CNY',
  };
  assert.equal(readConfig(env).host, '127.0.0.1');
  assert.throws(() => readConfig({ ...env, UNIHUB_CURRENCY: 'USD' }), /region|currency/i);
  assert.throws(() => readConfig({ ...env, UNIHUB_REGION: undefined }), /region|currency/i);
  assert.equal(readConfig({ ...env, UNIHUB_REGION: 'us', UNIHUB_CURRENCY: 'USD' }).currency, 'USD');

  const context = await fixture(t);
  const second = await buildApp(context.config);
  t.after(() => second.close());
  assert.equal((await second.inject('/health')).statusCode, 200);
  assert.equal((await context.pool.query('SELECT count(*)::int AS count FROM schema_migrations')).rows[0].count, 2);
  await assert.rejects(buildApp({ ...context.config, region: 'us', currency: 'USD' }), /region|currency/i);
});

test('OIDC 同身份重登复用用户和个人账户，同邮箱的不同主体保持隔离', async (t) => {
  const context = await fixture(t);
  const first = await signIn(context, 'same-subject');
  const again = await signIn(context, 'same-subject');
  const other = await signIn(context, 'other-subject');
  assert.equal(first.user.id, again.user.id);
  assert.equal(first.activeAccountId, again.activeAccountId);
  assert.notEqual(other.user.id, first.user.id);
  assert.notEqual(other.activeAccountId, first.activeAccountId);
});

test('应用登记隔离运营权限，并用注册回调和客户端凭证完成一次性登录交接', async (t) => {
  const context = await fixture(t);
  const { app } = context;
  const operator = await signIn(context, 'operator');
  const member = await signIn(context, 'app-operator');
  const create = (name, redirectUri, session = operator) => change(app, session, 'POST', '/api/admin/apps', {
    name, redirectUris: [redirectUri],
  });
  const redirect = 'https://a.example.test/callback';
  assert.equal((await create('bad', 'http://a.example.test/callback')).statusCode, 400);
  assert.equal((await create('forbidden', redirect, member)).statusCode, 403);
  const [createdA, createdB] = await Promise.all([create('应用 A', redirect), create('应用 B', 'https://b.example.test/callback')]);
  assert.equal(createdA.statusCode, 201);
  assert.equal(createdB.statusCode, 201);
  const a = createdA.json();
  const b = createdB.json();
  assert.notEqual(a.app.id, b.app.id);
  assert.ok(a.clientSecret);
  assert.equal((await change(app, operator, 'PUT', `/api/admin/apps/${a.app.id}/operators/${member.user.id}`)).statusCode, 204);
  const allowed = await app.inject({ url: `/api/admin/apps/${a.app.id}`, cookies: member.cookies });
  assert.equal(allowed.statusCode, 200);
  assert.equal(allowed.json().app.id, a.app.id);
  assert.equal(allowed.json().clientSecret, undefined);
  assert.equal((await app.inject({ url: `/api/admin/apps/${b.app.id}`, cookies: member.cookies })).statusCode, 403);
  const settings = { name: '应用 A', redirectUris: [redirect], enabled: true };
  assert.equal((await change(app, member, 'PUT', `/api/admin/apps/${a.app.id}`, settings)).statusCode, 403);
  assert.equal((await change(app, operator, 'DELETE', `/api/admin/apps/${a.app.id}/operators/${member.user.id}`)).statusCode, 204);
  assert.equal((await app.inject({ url: `/api/admin/apps/${a.app.id}`, cookies: member.cookies })).statusCode, 403);

  const loginUrl = (uri) => `/login?${new URLSearchParams({ app_id: a.app.id, redirect_uri: uri, state: 'app-state' })}`;
  assert.equal((await app.inject(loginUrl(`${redirect}/elsewhere`))).statusCode, 400);
  const attempt = await context.login('app-operator', loginUrl(redirect));
  const callback = await app.inject({ url: attempt.callback, cookies: attempt.cookies });
  const result = new URL(callback.headers.location);
  assert.equal(result.origin + result.pathname, redirect);
  assert.equal(result.searchParams.get('state'), 'app-state');
  const code = result.searchParams.get('code');
  assert.ok(code);

  const exchange = (client, redirectUri = redirect, loginCode = code) => app.inject({
    method: 'POST', url: '/api/apps/login/exchange',
    headers: { authorization: `Basic ${Buffer.from(`${client.app.id}:${client.clientSecret}`).toString('base64')}` },
    payload: { code: loginCode, redirectUri },
  });
  assert.equal((await exchange({ ...a, clientSecret: 'wrong' })).statusCode, 401);
  assert.equal((await exchange(b)).statusCode, 400);
  assert.equal((await exchange(a, `${redirect}/elsewhere`)).statusCode, 400);
  const outcomes = await Promise.all([exchange(a), exchange(a)]);
  assert.deepEqual(outcomes.map(({ statusCode }) => statusCode).sort(), [200, 400]);
  const accepted = outcomes.find(({ statusCode }) => statusCode === 200);
  assert.equal(accepted.json().userId, member.user.id);
  assert.equal((await exchange(a)).statusCode, 400);

  const newHandoff = await app.inject({ url: loginUrl(redirect), cookies: member.cookies });
  assert.equal(newHandoff.statusCode, 303);
  const nextCode = new URL(newHandoff.headers.location).searchParams.get('code');
  assert.ok(nextCode);
  assert.equal((await change(app, operator, 'PUT', `/api/admin/apps/${a.app.id}`, {
    ...settings, redirectUris: ['https://a.example.test/new-callback'],
  })).statusCode, 200);
  assert.equal((await exchange(a, redirect, nextCode)).statusCode, 400);
  assert.equal((await change(app, operator, 'PUT', `/api/admin/apps/${a.app.id}`, settings)).statusCode, 200);
  assert.equal((await exchange(a, redirect, nextCode)).statusCode, 400);
  const finalHandoff = await app.inject({ url: loginUrl(redirect), cookies: member.cookies });
  const finalCode = new URL(finalHandoff.headers.location).searchParams.get('code');
  assert.ok(finalCode);
  assert.equal((await change(app, member, 'POST', '/logout', { csrf: member.csrf })).statusCode, 303);
  assert.equal((await exchange(a, redirect, finalCode)).statusCode, 400);
});

test('企业撤权立即约束旧会话和付款账户，用户仍可切回个人', async (t) => {
  const context = await fixture(t);
  const { app } = context;
  const owner = await signIn(context, 'enterprise-owner');
  const member = await signIn(context, 'enterprise-member');
  const outsider = await signIn(context, 'enterprise-outsider');
  const company = await change(app, owner, 'POST', '/api/accounts/enterprises', { name: '星河企业' });
  assert.equal(company.statusCode, 201);
  const companyId = company.json().accountId;
  assert.equal((await app.inject({
    method: 'POST', url: '/api/session/account', cookies: owner.cookies, payload: { accountId: companyId },
  })).statusCode, 403);
  const memberUrl = `/api/accounts/${companyId}/members/${member.user.id}`;
  const update = (hasSeat) => change(app, owner, 'PUT', memberUrl, { role: 'member', canRecharge: false, canPurchase: true, hasSeat });
  assert.equal((await update(true)).statusCode, 204);
  assert.equal((await change(app, outsider, 'POST', '/api/session/account', { accountId: companyId })).statusCode, 403);
  assert.equal((await change(app, member, 'POST', '/api/session/account', { accountId: companyId })).statusCode, 200);
  const current = await app.inject({ url: '/api/session', cookies: member.cookies });
  assert.equal(current.json().activeAccountId, companyId);
  assert.match((await app.inject({ url: '/', cookies: member.cookies })).body, /星河企业/);

  const can = (action) => app.inject({ url: `/api/active-account/permissions/${action}`, cookies: member.cookies });
  assert.equal((await can('purchase')).statusCode, 200);
  assert.equal((await can('recharge')).statusCode, 403);
  assert.equal((await can('issue-key')).statusCode, 200);
  assert.equal((await update(false)).statusCode, 204);
  assert.equal((await can('issue-key')).statusCode, 403);
  assert.equal((await can('purchase')).statusCode, 403);
  assert.equal((await update(true)).statusCode, 204);
  assert.equal((await can('purchase')).statusCode, 200);
  assert.equal((await change(app, owner, 'DELETE', memberUrl)).statusCode, 204);
  assert.equal((await can('purchase')).statusCode, 403);
  assert.equal((await app.inject({ url: '/api/session', cookies: member.cookies })).json().activeAccount, null);
  assert.match((await app.inject({ url: '/', cookies: member.cookies })).body, /当前付款账户已不可用/);
  const personal = await app.inject({
    method: 'POST', url: '/api/session/account', cookies: member.cookies,
    headers: { 'content-type': 'application/x-www-form-urlencoded' },
    payload: new URLSearchParams({ accountId: member.activeAccountId, csrf: member.csrf }).toString(),
  });
  assert.equal(personal.statusCode, 303);
  assert.equal(personal.headers.location, '/');
  assert.equal((await can('purchase')).statusCode, 200);
});
