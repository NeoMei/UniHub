import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync, randomBytes, sign } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { test } from 'node:test';
import pg from 'pg';
import { buildApp } from '../src/app.js';

async function testDatabase(t) {
  const connectionString = process.env.UNIHUB_TEST_DATABASE_URL;
  assert.ok(connectionString, 'set UNIHUB_TEST_DATABASE_URL to a dedicated PostgreSQL test database');
  const control = new pg.Pool({ connectionString });
  const schema = `unihub_4_${randomBytes(8).toString('hex')}`;
  await control.query(`CREATE SCHEMA ${schema}`);
  const pool = new pg.Pool({ connectionString, options: `-c search_path=${schema}` });
  t.after(async () => {
    await pool.end();
    try { await control.query(`DROP SCHEMA ${schema} CASCADE`); }
    finally { await control.end(); }
  });
  return pool;
}

test('Stage 4: SPA 静态产物严格 CSP 托管、白标公开接口与多角色会话上下文', async (t) => {
  const pool = await testDatabase(t);
  const clientId = 'unihub-4-test';
  const { privateKey, publicKey } = generateKeyPairSync('ec', { namedCurve: 'secp384r1' });
  let attempt;
  let issuer;
  let subject = 'operator';
  const provider = createServer(async (request, response) => {
    const send = (status, value) => {
      response.writeHead(status, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify(value));
    };
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
        nonce: attempt.searchParams.get('nonce'), name: subject, email: `${subject}@example.test`,
      };
      const parts = [{ alg: 'ES384', kid: 'fixture' }, payload].map((item) => Buffer.from(JSON.stringify(item)).toString('base64url'));
      const unsigned = parts.join('.');
      const signature = sign('sha384', Buffer.from(unsigned), { key: privateKey, dsaEncoding: 'ieee-p1363' });
      return send(200, { token_type: 'Bearer', access_token: 'fixture-token', id_token: `${unsigned}.${signature.toString('base64url')}` });
    }
    return send(404, { error: 'not_found' });
  });
  await new Promise((resolve) => provider.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => provider.close(resolve)));
  issuer = `http://127.0.0.1:${provider.address().port}/oidc`;

  const app = await buildApp({
    origin: new URL('http://127.0.0.1:14000'), issuer: new URL(issuer), clientId,
    clientSecret: 'fixture-client-secret', sessionKey: randomBytes(32),
    pool, region: 'cn', currency: 'CNY', operatorSubjects: new Set(['operator']),
  });
  t.after(() => app.close());

  // 1. Check built web/dist/index.html is served with strict CSP & zero inline scripts / external CDN
  const distHtml = await readFile(new URL('../web/dist/index.html', import.meta.url), 'utf8');
  assert.doesNotMatch(distHtml, /https?:\/\//i);
  assert.doesNotMatch(distHtml, /<script(?![^>]*src=)/i);

  const rootRes = await app.inject('/');
  assert.equal(rootRes.statusCode, 200);
  assert.match(rootRes.headers['content-security-policy'], /script-src 'self'/);
  assert.doesNotMatch(rootRes.headers['content-security-policy'], /unsafe-inline/);
  assert.match(rootRes.body, /登录 UniHub/);

  // 2. Check asset MIME serving & traversal rejection
  const logoRes = await app.inject('/assets/unihub-logo-v1.png');
  assert.equal(logoRes.statusCode, 200);
  assert.equal(logoRes.headers['content-type'], 'image/png');
  const scriptSrc = distHtml.match(/src="(\/assets\/[^"]+)"/)?.[1];
  assert.ok(scriptSrc);
  const jsRes = await app.inject(scriptSrc);
  assert.equal(jsRes.statusCode, 200);
  assert.match(jsRes.headers['content-type'], /application\/javascript/);
  assert.equal((await app.inject('/assets/..%2fpackage.json')).statusCode, 404);

  // 3. Sign in as operator vs normal user -> verify /api/session & /api/apps/public white-label boundary
  async function signIn(userSubject, portal = '') {
    subject = userSubject;
    const loginRes = await app.inject('/login?portal=' + encodeURIComponent(portal));
    attempt = new URL(loginRes.headers.location);
    const state = attempt.searchParams.get('state');
    const loginCookie = loginRes.cookies[0];
    const cbRes = await app.inject({
      url: `/auth/callback?code=valid-code&state=${encodeURIComponent(state)}`,
      cookies: { [loginCookie.name]: loginCookie.value },
    });
    const sessionCookie = cbRes.cookies[0];
    const cookies = { [sessionCookie.name]: sessionCookie.value };
    const sessionRes = await app.inject({ url: '/api/session', cookies });
    return { cookies, returnTo: cbRes.headers.location, ...sessionRes.json() };
  }

  const op = await signIn('operator', 'paylab');
  assert.equal(op.returnTo, '/paylab');
  assert.equal((await app.inject('/internal/portals/operator')).statusCode, 401);
  assert.equal((await app.inject({ url: '/internal/portals/operator', cookies: op.cookies })).statusCode, 204);
  const frame = await app.inject({ url: '/llmrouter', cookies: op.cookies });
  assert.equal(frame.statusCode, 200);
  assert.match(frame.body, /src="https:\/\/llmrouter\.quukk\.com\/dashboard"/);
  assert.match(frame.headers['content-security-policy'], /frame-src https:\/\/llmrouter\.quukk\.com;/);
  assert.equal((await app.inject({ url: '/login?portal=auth', cookies: op.cookies })).headers.location, '/auth');
  const anonymous = await app.inject({
    url: '/llmrouter', headers: { 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'document' },
  });
  assert.equal(anonymous.statusCode, 401);
  assert.match(anonymous.body, /href="\/login\?portal=llmrouter"/);
  const apiDenied = await app.inject({
    url: '/internal/portals/login', headers: { 'x-original-uri': '/paylab/graphql' },
  });
  assert.equal(apiDenied.statusCode, 401);
  assert.equal(apiDenied.json().error, 'login_required');
  assert.equal(op.isPlatformOperator, true);
  assert.equal(op.region, 'cn');
  assert.equal(op.currency, 'CNY');

  await app.inject({
    method: 'POST', url: '/api/admin/apps', cookies: op.cookies, headers: { 'x-csrf-token': op.csrf },
    payload: { name: 'SuperWagie', description: 'AI Agent Hub', redirectUris: ['https://sw.example.test/cb'] },
  });

  const pubApps = await app.inject('/api/apps/public');
  assert.equal(pubApps.statusCode, 200);
  assert.equal(pubApps.json().apps.length, 1);
  assert.equal(pubApps.json().apps[0].clientSecret, undefined);
  assert.equal(pubApps.json().apps[0].redirectUris, undefined);

  const member = await signIn('end-user', '//untrusted.example');
  assert.equal(member.returnTo, '/');
  assert.equal((await app.inject({ url: '/internal/portals/operator', cookies: member.cookies })).statusCode, 403);
  assert.equal((await app.inject({ url: '/llmrouter', cookies: member.cookies })).statusCode, 403);
  assert.equal(member.isPlatformOperator, false);
  assert.deepEqual(member.operatorApps, []);

  // All SPA routes return 200 HTML shell
  for (const spaUrl of ['/docs', '/about', '/brand', '/console', '/console/store', '/admin/commerce', '/invite/sample']) {
    const res = await app.inject({ url: spaUrl, cookies: member.cookies });
    assert.equal(res.statusCode, 200, spaUrl);
    assert.match(res.headers['content-type'], /text\/html/);
  }
});
