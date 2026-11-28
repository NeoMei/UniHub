import assert from 'node:assert/strict';
import { createHash, createSign, createVerify, generateKeyPairSync, randomBytes, sign } from 'node:crypto';
import { createServer } from 'node:http';
import { test } from 'node:test';
import pg from 'pg';
import { buildApp } from '../src/app.js';
import { readConfig } from '../src/config.js';
import { verifyAlipayRsa2 } from '../src/payments/verifiers.js';
import { startBackgroundWorkers } from '../src/worker.js';

async function testDatabase(t) {
  const connectionString = process.env.UNIHUB_TEST_DATABASE_URL;
  assert.ok(connectionString, 'set UNIHUB_TEST_DATABASE_URL to a dedicated PostgreSQL test database');
  const control = new pg.Pool({ connectionString });
  const schema = `unihub_5_${randomBytes(8).toString('hex')}`;
  await control.query(`CREATE SCHEMA ${schema}`);
  const pool = new pg.Pool({ connectionString, options: `-c search_path=${schema}` });
  t.after(async () => {
    await pool.end();
    try { await control.query(`DROP SCHEMA ${schema} CASCADE`); }
    finally { await control.end(); }
  });
  return pool;
}

test('Stage 5: readConfig 完整解析生产环境变量、默认关闭测试沙箱并验证微信/支付宝/Stripe 出站下单', async (t) => {
  const mchKeys = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const wxPlatKeys = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const aliKeys = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const sessionKeyB64 = randomBytes(32).toString('base64url');

  // Mock WeChat v3 + Stripe servers
  let stripeFormSeen = null;
  let wxAuthSeen = '';
  const payMock = createServer(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const raw = Buffer.concat(chunks).toString('utf8');
    if (request.url === '/v3/pay/transactions/native' && request.method === 'POST') {
      wxAuthSeen = request.headers.authorization ?? '';
      const respBody = JSON.stringify({ code_url: 'weixin://wxpay/bizpayurl?pr=mockcode123' });
      const ts = String(Math.floor(Date.now() / 1000));
      const nonce = 'respnonce1';
      const signer = createSign('RSA-SHA256');
      signer.update(`${ts}\n${nonce}\n${respBody}\n`, 'utf8');
      signer.end();
      response.writeHead(200, {
        'content-type': 'application/json',
        'wechatpay-timestamp': ts,
        'wechatpay-nonce': nonce,
        'wechatpay-serial': 'WX-PLAT-1',
        'wechatpay-signature': signer.sign(wxPlatKeys.privateKey, 'base64'),
      });
      return response.end(respBody);
    }
    if (request.url === '/v1/checkout/sessions' && request.method === 'POST') {
      stripeFormSeen = new URLSearchParams(raw);
      response.writeHead(200, { 'content-type': 'application/json' });
      return response.end(JSON.stringify({ id: 'cs_live_test_1', url: 'https://checkout.stripe.test/pay/cs_live_test_1' }));
    }
    response.writeHead(404);
    response.end();
  });
  await new Promise((resolve) => payMock.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => payMock.close(resolve)));
  const mockOrigin = `http://127.0.0.1:${payMock.address().port}`;

  const cfg = readConfig({
    UNIHUB_ORIGIN: 'https://hub.example.test',
    LOGTO_ISSUER: 'https://auth.example.test/oidc',
    LOGTO_CLIENT_ID: 'cid',
    LOGTO_CLIENT_SECRET: 'csec',
    UNIHUB_SESSION_KEY: sessionKeyB64,
    DATABASE_URL: 'postgresql://postgres@127.0.0.1:5433/unihub_v2_cn',
    UNIHUB_REGION: 'cn',
    UNIHUB_CURRENCY: 'CNY',
    UNIHUB_OPERATOR_SUBJECTS: '["op-1"]',
    UNIHUB_GATEWAY_SERVICE_TOKEN: 'gw-tok',
    UNIHUB_FX_RATES_JSON: '{"USD":"7.20"}',
    LAGO_ENABLED: 'true',
    LAGO_API_URL: 'http://127.0.0.1:18781',
    LAGO_API_KEY: 'lago-key',
    WECHAT_PLATFORM_SERIAL: 'WX-PLAT-1',
    WECHAT_PLATFORM_PUBLIC_KEY: wxPlatKeys.publicKey.export({ type: 'spki', format: 'pem' }).replace(/\n/g, '\\n'),
    WECHAT_APIV3_KEY: '0123456789abcdef0123456789abcdef',
    WECHAT_APP_ID: 'wxapp1',
    WECHAT_MCH_ID: 'wxmch1',
    WECHAT_CERT_SERIAL: 'WXMCHCERT1',
    WECHAT_PRIVATE_KEY: mchKeys.privateKey.export({ type: 'pkcs8', format: 'pem' }).replace(/\n/g, '\\n'),
    WECHAT_NOTIFY_URL: 'https://hub.example.test/api/webhooks/payments/wechat',
    WECHAT_API_BASE_URL: mockOrigin,
    ALIPAY_PUBLIC_KEY: aliKeys.publicKey.export({ type: 'spki', format: 'pem' }),
    ALIPAY_APP_ID: 'aliapp1',
    ALIPAY_PRIVATE_KEY: aliKeys.privateKey.export({ type: 'pkcs8', format: 'pem' }),
    ALIPAY_NOTIFY_URL: 'https://hub.example.test/api/webhooks/payments/alipay',
    ALIPAY_RETURN_URL: 'https://hub.example.test/console/wallet',
  });

  assert.equal(cfg.paymentConfig.allowSandboxSettle, false);
  assert.equal(cfg.gatewayServiceToken, 'gw-tok');
  assert.deepEqual(cfg.fxRates, { USD: '7.20' });
  assert.equal(cfg.lagoConfig.enabled, true);
  assert.equal(cfg.workerConfig.enabled, true);

  // Spin up OIDC fixture + real PostgreSQL with production readConfig output
  const pool = await testDatabase(t);
  const { privateKey: oidcPriv, publicKey: oidcPub } = generateKeyPairSync('ec', { namedCurve: 'secp384r1' });
  let attempt;
  let issuer;
  const oidcSrv = createServer(async (request, response) => {
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
    if (request.url === '/oidc/jwks') return send(200, { keys: [{ ...oidcPub.export({ format: 'jwk' }), kid: 'k1', alg: 'ES384' }] });
    if (request.url === '/oidc/token' && request.method === 'POST') {
      const chunks = [];
      for await (const c of request) chunks.push(c);
      const b = new URLSearchParams(Buffer.concat(chunks).toString());
      const ch = createHash('sha256').update(b.get('code_verifier') ?? '').digest('base64url');
      if (b.get('code') !== 'valid' || ch !== attempt?.searchParams.get('code_challenge')) return send(400, { error: 'invalid_grant' });
      const now = Math.floor(Date.now() / 1000);
      const pl = { iss: issuer, aud: 'cid', sub: 'u1', iat: now, exp: now + 600, nonce: attempt.searchParams.get('nonce'), name: 'U1', email: 'u1@example.test' };
      const unsigned = [{ alg: 'ES384', kid: 'k1' }, pl].map((x) => Buffer.from(JSON.stringify(x)).toString('base64url')).join('.');
      const sig = sign('sha384', Buffer.from(unsigned), { key: oidcPriv, dsaEncoding: 'ieee-p1363' });
      return send(200, { token_type: 'Bearer', access_token: 'fixture-token', id_token: `${unsigned}.${sig.toString('base64url')}` });
    }
    send(404, {});
  });
  await new Promise((resolve) => oidcSrv.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => oidcSrv.close(resolve)));
  issuer = `http://127.0.0.1:${oidcSrv.address().port}/oidc`;

  const app = await buildApp({
    ...cfg,
    origin: new URL('http://127.0.0.1:14000'),
    issuer: new URL(issuer),
    pool,
  });
  t.after(() => app.close());

  const loginRes = await app.inject('/login');
  attempt = new URL(loginRes.headers.location);
  const cbRes = await app.inject({
    url: `/auth/callback?code=valid&state=${encodeURIComponent(attempt.searchParams.get('state'))}`,
    cookies: { [loginRes.cookies[0].name]: loginRes.cookies[0].value },
  });
  assert.equal(cbRes.headers.location, '/');
  const cookies = { [cbRes.cookies[0].name]: cbRes.cookies[0].value };
  const sessionRes = await app.inject({ url: '/api/session', cookies });
  assert.equal(sessionRes.statusCode, 200, sessionRes.body);
  const session = sessionRes.json();

  // WeChat outbound checkout returns real codeUrl & signs header cleanly
  const wxRes = await app.inject({
    method: 'POST', url: '/api/wallet/recharges', cookies, headers: { 'x-csrf-token': session.csrf },
    payload: { provider: 'wechat', merchantId: 'wxmch1', amountMinor: 9900 },
  });
  assert.equal(wxRes.statusCode, 201, wxRes.body);
  assert.equal(wxRes.json().checkoutSession.codeUrl, 'weixin://wxpay/bizpayurl?pr=mockcode123');
  assert.match(wxAuthSeen, /WECHATPAY2-SHA256-RSA2048/);

  // sandbox-settle is blocked in production mode (allowSandboxSettle: false)
  const sbBlocked = await app.inject({
    method: 'POST',
    url: `/api/wallet/payment-orders/${wxRes.json().paymentOrder.id}/sandbox-settle`,
    cookies,
    headers: { 'x-csrf-token': session.csrf },
    payload: { providerTradeNo: 'blocked' },
  });
  assert.equal(sbBlocked.statusCode, 403);
  assert.equal(sbBlocked.json().error, 'sandbox_disabled');

  // Alipay outbound checkout generates verifiable RSA2 redirect URL
  const aliRes = await app.inject({
    method: 'POST', url: '/api/wallet/recharges', cookies, headers: { 'x-csrf-token': session.csrf },
    payload: { provider: 'alipay', merchantId: 'ali1', amountMinor: 1234 },
  });
  assert.equal(aliRes.statusCode, 201, aliRes.body);
  const aliRedirect = new URL(aliRes.json().checkoutSession.redirectUrl);
  const aliParams = Object.fromEntries(aliRedirect.searchParams.entries());
  assert.equal(JSON.parse(aliParams.biz_content).total_amount, '12.34');
  // Outbound Alipay request signs ALL non-empty params except "sign" (including sign_type=RSA2)
  const aliOutboundSignStr = Object.keys(aliParams)
    .filter((k) => k !== 'sign' && aliParams[k] !== '')
    .sort()
    .map((k) => `${k}=${aliParams[k]}`)
    .join('&');
  const aliVerify = createVerify('RSA-SHA256');
  aliVerify.update(aliOutboundSignStr, 'utf8');
  aliVerify.end();
  assert.equal(aliVerify.verify(aliKeys.publicKey, Buffer.from(aliParams.sign, 'base64')), true);
  // Also double-check notification verifier works for async webhook (which omits sign_type)
  assert.equal(verifyAlipayRsa2({
    params: { ...aliParams, sign: createSign('RSA-SHA256').update(
      Object.keys(aliParams).filter((k) => k !== 'sign' && k !== 'sign_type').sort().map((k) => `${k}=${aliParams[k]}`).join('&'),
      'utf8',
    ).sign(aliKeys.privateKey, 'base64') },
    alipayPublicKey: aliKeys.publicKey.export({ type: 'spki', format: 'pem' }),
  }).valid, true);

  // Background worker runOnce + clean stop check
  const workers = startBackgroundWorkers(pool, {
    region: 'cn', currency: 'CNY', workerConfig: { enabled: true, outboxIntervalMs: 10_000 },
  });
  await workers.runOnce();
  workers.stop();

  // Stripe outbound checkout unit check on US config
  const usPool = await testDatabase(t);
  const usCfg = readConfig({
    UNIHUB_ORIGIN: 'https://us.example.test',
    LOGTO_ISSUER: 'https://auth-us.example.test/oidc',
    LOGTO_CLIENT_ID: 'cid',
    LOGTO_CLIENT_SECRET: 'csec',
    UNIHUB_SESSION_KEY: sessionKeyB64,
    DATABASE_URL: 'postgresql://postgres@127.0.0.1:5433/unihub_v2_us',
    UNIHUB_REGION: 'us',
    UNIHUB_CURRENCY: 'USD',
    STRIPE_SECRET_KEY: 'sk_test_123',
    STRIPE_SUCCESS_URL: 'https://us.example.test/console/wallet',
    STRIPE_API_BASE_URL: mockOrigin,
  });
  const usApp = await buildApp({ ...usCfg, origin: new URL('http://127.0.0.1:14001'), issuer: new URL(issuer), pool: usPool });
  t.after(() => usApp.close());
  const usLogin = await usApp.inject('/login');
  attempt = new URL(usLogin.headers.location);
  const usCb = await usApp.inject({
    url: `/auth/callback?code=valid&state=${encodeURIComponent(attempt.searchParams.get('state'))}`,
    cookies: { [usLogin.cookies[0].name]: usLogin.cookies[0].value },
  });
  assert.equal(usCb.headers.location, '/');
  const usCookies = { [usCb.cookies[0].name]: usCb.cookies[0].value };
  const usSessRes = await usApp.inject({ url: '/api/session', cookies: usCookies });
  assert.equal(usSessRes.statusCode, 200, usSessRes.body);
  const usSess = usSessRes.json();
  const stRes = await usApp.inject({
    method: 'POST', url: '/api/wallet/recharges', cookies: usCookies, headers: { 'x-csrf-token': usSess.csrf },
    payload: { provider: 'stripe', amountMinor: 2500 },
  });
  assert.equal(stRes.statusCode, 201, stRes.body);
  assert.equal(stRes.json().checkoutSession.redirectUrl, 'https://checkout.stripe.test/pay/cs_live_test_1');
  assert.equal(stripeFormSeen.get('line_items[0][price_data][currency]'), 'usd');
  assert.equal(stripeFormSeen.get('line_items[0][price_data][unit_amount]'), '2500');
});
