import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync, randomBytes, sign } from 'node:crypto';
import { createServer } from 'node:http';
import { test } from 'node:test';
import pg from 'pg';
import { buildApp } from '../src/app.js';
import { computeTwoLayerUnits, reconcileExpiredHolds } from '../src/gateway/billing-v1.js';
import { seedSuperWagie } from '../src/seed/superwagie.js';

async function testDatabase(t) {
  const connectionString = process.env.UNIHUB_TEST_DATABASE_URL;
  assert.ok(connectionString, 'set UNIHUB_TEST_DATABASE_URL to a dedicated PostgreSQL test database');
  const control = new pg.Pool({ connectionString });
  const schema = `unihub_3_${randomBytes(8).toString('hex')}`;
  await control.query(`CREATE SCHEMA ${schema}`);
  const pool = new pg.Pool({ connectionString, options: `-c search_path=${schema}` });
  t.after(async () => {
    await pool.end();
    try { await control.query(`DROP SCHEMA ${schema} CASCADE`); }
    finally { await control.end(); }
  });
  return pool;
}

async function fixture(t, {
  region = 'cn', currency = 'CNY', gatewayServiceToken = 'gw-svc-secret-token',
  fxRates = null, lagoHandler = null,
} = {}) {
  const pool = await testDatabase(t);
  const clientId = 'unihub-3-test';
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

  let lagoConfig = {};
  if (lagoHandler) {
    const lagoServer = createServer(async (request, response) => {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const raw = Buffer.concat(chunks).toString();
      const body = raw ? JSON.parse(raw) : {};
      const out = await lagoHandler({ method: request.method, url: request.url, body });
      response.writeHead(out.status ?? 200, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify(out.body ?? {}));
    });
    await new Promise((resolve) => lagoServer.listen(0, '127.0.0.1', resolve));
    t.after(() => new Promise((resolve) => lagoServer.close(resolve)));
    lagoConfig = { enabled: true, baseUrl: `http://127.0.0.1:${lagoServer.address().port}`, apiKey: 'lago-test-key' };
  }

  const app = await buildApp({
    origin: new URL('http://127.0.0.1:14000'), issuer: new URL(issuer), clientId,
    clientSecret: 'fixture-client-secret', sessionKey: randomBytes(32),
    pool, region, currency, operatorSubjects: new Set(['operator']),
    gatewayServiceToken, fxRates, lagoConfig,
  });
  t.after(() => app.close());

  async function signIn(userSubject) {
    subject = userSubject;
    const loginRes = await app.inject('/login');
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
    return { cookies, ...sessionRes.json() };
  }

  function req(session, method, url, payload) {
    return app.inject({
      method, url, cookies: session.cookies, payload,
      headers: method === 'GET' ? {} : { 'x-csrf-token': session.csrf },
    });
  }

  function gw(method, url, payload, {
    token = gatewayServiceToken, version = '1.0', reqRegion = region, reqCurrency = currency,
  } = {}) {
    return app.inject({
      method,
      url,
      payload,
      headers: {
        'x-unihub-service-token': token,
        'x-gateway-version': version,
        'x-unihub-region': reqRegion,
        'x-unihub-currency': reqCurrency,
      },
    });
  }

  async function topUp(session, amountMinor, tradeNo) {
    const pay = await req(session, 'POST', '/api/wallet/recharges', { amountMinor });
    assert.equal(pay.statusCode, 201, pay.body);
    await req(session, 'POST', `/api/wallet/payment-orders/${pay.json().paymentOrder.id}/sandbox-settle`, {
      providerTradeNo: tradeNo,
    });
  }

  return { app, pool, signIn, req, gw, topUp };
}

test('Stage 3: 统一网关 Key、两层精算 (135→90/45 & 5类云API)、锁版隔夜调价、超额平台差与 402 拒截断', async (t) => {
  // Exact 2-layer math check from docs/architecture.md:
  // 3 images * 0.20 * 1.25 = 0.75 * 0.90 * 200 = 135 credits -> settle 2 images (0.50) = 90 credits
  assert.equal(computeTwoLayerUnits({
    baseAmount: '0.75', providerCurrency: 'CNY', regionCurrency: 'CNY',
    unihubMultiplier: '0.90', creditsPerCurrency: 200, roundingMode: 'ceil',
  }).units, 135_000_000n);
  assert.equal(computeTwoLayerUnits({
    baseAmount: '0.50', providerCurrency: 'CNY', regionCurrency: 'CNY',
    unihubMultiplier: '0.90', creditsPerCurrency: 200, roundingMode: 'round',
  }).units, 90_000_000n);
  // 0.0040 credits precision check
  assert.equal(computeTwoLayerUnits({
    baseAmount: '0.000020', providerCurrency: 'CNY', regionCurrency: 'CNY',
    unihubMultiplier: '1.00', creditsPerCurrency: 200, roundingMode: 'round',
  }).units, 4_000n);

  const ctx = await fixture(t);
  const operator = await ctx.signIn('operator');
  const user = await ctx.signIn('gw-personal');
  const { app: swApp } = await seedSuperWagie(ctx.pool, {
    operatorUserId: operator.user.id, region: 'cn', currency: 'CNY',
  });

  // Update personal route pricing on seedream-4.0 to 0.90 multiplier matching example
  await ctx.req(operator, 'POST', `/api/admin/apps/${swApp.id}/route-pricing/publish`, {
    accountKind: 'personal',
    creditsPerCurrency: 200,
    routes: [
      { modelId: 'text-embedding-v4', routeId: 'ali:text-embedding-v4', operation: 'embedding', unit: '1K tokens', minBillableQty: '0.04', meterVersion: 'm_ali_emb_v4', routerRateVersion: 'r_ali_v1', baseAmountDecimal: '0.0005', unihubMultiplier: '1.00' },
      { modelId: 'gte-rerank-v2', routeId: 'ali:gte-rerank-v2', operation: 'rerank', unit: '1K tokens', minBillableQty: '0.1', meterVersion: 'm_ali_rerank_v2', routerRateVersion: 'r_ali_v1', baseAmountDecimal: '0.001', unihubMultiplier: '1.10' },
      { modelId: 'seedream-4.0', routeId: 'volc:seedream-4.0', operation: 'image_generation', unit: '张', minBillableQty: '1', meterVersion: 'm_volc_seedream_v1', routerRateVersion: 'r_volc_v1', baseAmountDecimal: '0.25', unihubMultiplier: '0.90' },
      { modelId: 'qwen3-vl-embedding', routeId: 'ali:qwen3-vl-embedding', operation: 'multimodal_embedding', unit: '1K tokens', minBillableQty: '0.1', meterVersion: 'm_ali_vl_emb_v1', routerRateVersion: 'r_ali_v1', baseAmountDecimal: '0.002', unihubMultiplier: '1.10' },
      { modelId: 'paddleocr-vl', routeId: 'baidu:paddleocr-vl', operation: 'ocr_parse', unit: '页', minBillableQty: '1', meterVersion: 'm_baidu_ocr_vl_v1', routerRateVersion: 'r_baidu_v1', baseAmountDecimal: '0.03', unihubMultiplier: '1.20' },
    ],
  });

  await ctx.topUp(user, 9900, 'topup-gw-1');
  await ctx.req(user, 'POST', '/api/billing/checkout', { appId: swApp.id, tierId: 'pro_monthly', quantity: 1 });
  await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});

  // Issue unified API key + rotate test
  const keyRes1 = await ctx.req(user, 'POST', '/api/keys', { appId: swApp.id, name: 'CI Key' });
  assert.equal(keyRes1.statusCode, 201);
  const oldPlain = keyRes1.json().plaintextKey;
  assert.ok(oldPlain.startsWith('uh_live_'));

  const rotRes = await ctx.req(user, 'POST', `/api/keys/${keyRes1.json().key.id}/rotate`, {});
  assert.equal(rotRes.statusCode, 201);
  const apiKey = rotRes.json().plaintextKey;

  // Old rotated key fails catalog
  assert.equal((await ctx.gw('POST', '/internal/billing/v1/catalog', { apiKey: oldPlain })).statusCode, 401);

  // Rolling version window & region/currency & auth checks
  assert.equal((await ctx.gw('POST', '/internal/billing/v1/catalog', { apiKey }, { token: 'wrong' })).statusCode, 401);
  assert.equal((await ctx.gw('POST', '/internal/billing/v1/catalog', { apiKey }, { version: '0.9' })).statusCode, 400);
  assert.equal((await ctx.gw('POST', '/internal/billing/v1/catalog', { apiKey }, { reqRegion: 'us', reqCurrency: 'USD' })).statusCode, 400);

  // Valid catalog under gateway 1.1 returns all 5 cloud API models
  const catRes = await ctx.gw('POST', '/internal/billing/v1/catalog', { apiKey }, { version: '1.1' });
  assert.equal(catRes.statusCode, 200);
  assert.equal(catRes.json().models.length, 5);

  // Foreign currency without explicit FX rule rejected
  const badFx = await ctx.gw('POST', '/internal/billing/v1/holds', {
    apiKey, requestId: 'req-fx-err', routeId: 'volc:seedream-4.0',
    meterVersion: 'm_volc_seedream_v1', routerRateVersion: 'r_volc_v1',
    baseAmount: '0.10', providerCurrency: 'USD',
  });
  assert.equal(badFx.statusCode, 400);
  assert.equal(badFx.json().error, 'fx_rule_missing');

  // Seedream 3-image hold (135 credits) -> commit -> route price changes AFTER hold -> settle 2 images uses FROZEN 0.90 multiplier (90 credits)!
  const holdImg = await ctx.gw('POST', '/internal/billing/v1/holds', {
    apiKey, requestId: 'req-seedream-1', routeId: 'volc:seedream-4.0',
    meterVersion: 'm_volc_seedream_v1', routerRateVersion: 'r_volc_v1',
    baseAmount: '0.75', providerCurrency: 'CNY', officialAmount: { amount: '0.60', currency: 'CNY' },
  });
  assert.equal(holdImg.statusCode, 201, holdImg.body);
  assert.equal(holdImg.json().hold.authorizedUnits, '135000000');
  const holdId = holdImg.json().hold.id;

  const commitRes = await ctx.gw('POST', `/internal/billing/v1/holds/${holdId}/commit`, {
    executorId: 'worker-1', attempt: 1, upstreamRef: 'ark-job-9001',
  });
  assert.equal(commitRes.statusCode, 200);
  assert.equal(commitRes.json().hold.status, 'in_flight');
  // Conflicting executor on same attempt gets 409
  const conflictCommit = await ctx.gw('POST', `/internal/billing/v1/holds/${holdId}/commit`, {
    executorId: 'worker-2', attempt: 1,
  });
  assert.equal(conflictCommit.statusCode, 409);

  // Operator doubles seedream-4.0 multiplier to 2.00 AFTER hold committed
  await ctx.req(operator, 'POST', `/api/admin/apps/${swApp.id}/route-pricing/publish`, {
    accountKind: 'personal',
    creditsPerCurrency: 200,
    routes: [
      { modelId: 'seedream-4.0', routeId: 'volc:seedream-4.0', operation: 'image_generation', unit: '张', minBillableQty: '1', meterVersion: 'm_volc_seedream_v1', routerRateVersion: 'r_volc_v1', baseAmountDecimal: '0.25', unihubMultiplier: '2.00' },
      { modelId: 'text-embedding-v4', routeId: 'ali:text-embedding-v4', operation: 'embedding', unit: '1K tokens', minBillableQty: '0.04', meterVersion: 'm_ali_emb_v4', routerRateVersion: 'r_ali_v1', baseAmountDecimal: '0.0005', unihubMultiplier: '1.00' },
      { modelId: 'gte-rerank-v2', routeId: 'ali:gte-rerank-v2', operation: 'rerank', unit: '1K tokens', minBillableQty: '0.1', meterVersion: 'm_ali_rerank_v2', routerRateVersion: 'r_ali_v1', baseAmountDecimal: '0.001', unihubMultiplier: '1.10' },
      { modelId: 'qwen3-vl-embedding', routeId: 'ali:qwen3-vl-embedding', operation: 'multimodal_embedding', unit: '1K tokens', minBillableQty: '0.1', meterVersion: 'm_ali_vl_emb_v1', routerRateVersion: 'r_ali_v1', baseAmountDecimal: '0.002', unihubMultiplier: '1.10' },
      { modelId: 'paddleocr-vl', routeId: 'baidu:paddleocr-vl', operation: 'ocr_parse', unit: '页', minBillableQty: '1', meterVersion: 'm_baidu_ocr_vl_v1', routerRateVersion: 'r_baidu_v1', baseAmountDecimal: '0.03', unihubMultiplier: '1.20' },
    ],
  });

  // Settle in-flight hold at frozen 0.90 multiplier (0.50 * 0.90 * 200 = 90 credits settled, 45 released)
  const settleImg = await ctx.gw('POST', `/internal/billing/v1/holds/${holdId}/settle`, {
    baseAmount: '0.50', providerCurrency: 'CNY',
    actualUsage: { generatedImages: 2 },
  });
  assert.equal(settleImg.statusCode, 200, settleImg.body);
  assert.equal(settleImg.json().hold.settledUnits, '90000000');
  assert.equal(settleImg.json().hold.releasedUnits, '45000000');

  // Remaining 4 cloud API routes + sub-0.01 precision + overage cap
  for (const [reqId, routeId, meterV, routerV, baseAmt, settleAmt, expectedSettled] of [
    ['r-emb', 'ali:text-embedding-v4', 'm_ali_emb_v4', 'r_ali_v1', '0.000020', '0.000020', '4000'],
    ['r-rerank', 'ali:gte-rerank-v2', 'm_ali_rerank_v2', 'r_ali_v1', '0.005', '0.004', '880000'],
    ['r-vlemb', 'ali:qwen3-vl-embedding', 'm_ali_vl_emb_v1', 'r_ali_v1', '0.01', '0.01', '2200000'],
    ['r-ocr', 'baidu:paddleocr-vl', 'm_baidu_ocr_vl_v1', 'r_baidu_v1', '0.10', '0.15', '24000000'], // overage 0.15 -> capped at authorized 0.10 (24M units)
  ]) {
    const h = await ctx.gw('POST', '/internal/billing/v1/holds', {
      apiKey, requestId: reqId, routeId, meterVersion: meterV, routerRateVersion: routerV,
      baseAmount: baseAmt, providerCurrency: 'CNY',
    });
    assert.equal(h.statusCode, 201, h.body);
    const s = await ctx.gw('POST', `/internal/billing/v1/holds/${h.json().hold.id}/settle`, {
      baseAmount: settleAmt, providerCurrency: 'CNY',
    });
    assert.equal(s.statusCode, 200);
    assert.equal(s.json().hold.settledUnits, expectedSettled);
  }

  // Overage recorded in platform_cost_differences
  const diffs = (await ctx.req(operator, 'GET', '/api/admin/platform-differences')).json().differences;
  assert.equal(diffs.length, 1);
  assert.equal(diffs[0].reason, 'overage');
  assert.equal(diffs[0].expectedUnits, '36000000');
  assert.equal(diffs[0].settledUnits, '24000000');
  assert.equal(diffs[0].diffUnits, '12000000');

  // Insufficient credits directly returns 402 (no max_tokens truncation!)
  const lowHold = await ctx.gw('POST', '/internal/billing/v1/holds', {
    apiKey, requestId: 'r-too-big', routeId: 'volc:seedream-4.0',
    meterVersion: 'm_volc_seedream_v1', routerRateVersion: 'r_volc_v1',
    baseAmount: '1000', providerCurrency: 'CNY',
  });
  assert.equal(lowHold.statusCode, 402);
  assert.equal(lowHold.json().error, 'insufficient_credits');

  // Admin runtime_settings blocks secret names and accepts safe settings
  const badSetting = await ctx.req(operator, 'PUT', '/api/admin/settings', {
    key: 'LOGTO_CLIENT_SECRET', value: 'bad',
  });
  assert.equal(badSetting.statusCode, 400);
  const okSetting = await ctx.req(operator, 'PUT', '/api/admin/settings', {
    key: 'proxy_display_domain', value: 'api.unihub.example.test',
  });
  assert.equal(okSetting.statusCode, 200);
  const overview = (await ctx.req(operator, 'GET', '/api/admin/overview')).json();
  assert.equal(overview.platformDifferencesCount, 1);
});

test('Stage 3: 企业撤席 vs 在途原周期结算、并发额度竞争、补占、TTL 巡检与 Lago 断连隔离恢复', async (t) => {
  let lagoDown = true;
  const lagoCalls = [];
  const ctx = await fixture(t, {
    lagoHandler: async ({ method, url, body }) => {
      lagoCalls.push({ method, url, body });
      if (lagoDown) return { status: 503, body: { error: 'lago_unavailable' } };
      if (url === '/api/v1/customers' && method === 'POST') return { status: 200, body: { customer: body.customer } };
      if (url === '/api/v1/invoices' && method === 'POST') {
        assert.equal(body.invoice.skip_psp, true);
        return { status: 200, body: { invoice: { lago_id: 'lago-inv-001', ...body.invoice } } };
      }
      if (url.startsWith('/api/v1/invoices/') && method === 'PUT') {
        assert.equal(body.invoice.payment_status, 'succeeded');
        return { status: 200, body: { invoice: { lago_id: 'lago-inv-001', payment_status: 'succeeded' } } };
      }
      return { status: 404 };
    },
  });

  const operator = await ctx.signIn('operator');
  const owner = await ctx.signIn('ent-3-owner');
  const member = await ctx.signIn('ent-3-member');
  const { app: swApp } = await seedSuperWagie(ctx.pool, {
    operatorUserId: operator.user.id, region: 'cn', currency: 'CNY',
  });

  const entId = (await ctx.req(owner, 'POST', '/api/accounts/enterprises', { name: 'Gateway Ent' })).json().accountId;
  await ctx.req(owner, 'PUT', `/api/accounts/${entId}/members/${member.user.id}`, {
    role: 'member', canRecharge: false, canPurchase: false, hasSeat: false,
  });
  await ctx.req(owner, 'POST', '/api/session/account', { accountId: entId });
  await ctx.req(member, 'POST', '/api/session/account', { accountId: entId });

  // Lago is DOWN during enterprise checkout -> checkout STILL succeeds locally + credits granted!
  await ctx.topUp(owner, 50000, 'topup-ent-3');
  const coRes = await ctx.req(owner, 'POST', '/api/billing/checkout', {
    appId: swApp.id, tierId: 'ent_monthly', quantity: 2,
  });
  assert.equal(coRes.statusCode, 201, coRes.body);
  const entOrderId = coRes.json().order.id;
  assert.equal(coRes.json().order.lagoSyncStatus, 'pending');

  // Outbox grants entitlement cleanly even though lago_invoice_sync topic fails while Lago is down
  const obRes = await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});
  assert.equal(obRes.json().processed, 1);
  assert.equal(obRes.json().failed.length, 1);
  assert.equal((await ctx.req(owner, 'GET', `/api/billing/orders/${entOrderId}`)).json().order.lagoSyncStatus, 'failed');

  // Lago comes back online -> retry succeeds and sets payment_status: 'succeeded' with skip_psp: true
  lagoDown = false;
  const retryRes = await ctx.req(operator, 'POST', `/api/admin/lago/retry/${entOrderId}`, {});
  assert.equal(retryRes.statusCode, 200, retryRes.body);
  assert.equal(retryRes.json().status, 'synced');
  assert.equal((await ctx.req(owner, 'GET', `/api/billing/orders/${entOrderId}`)).json().order.lagoSyncStatus, 'synced');

  const entSub = (await ctx.req(owner, 'GET', '/api/billing/subscriptions')).json().subscriptions[0];
  // Even enterprise owner CANNOT issue API key without explicit per-app seat!
  assert.equal((await ctx.req(owner, 'POST', '/api/keys', { appId: swApp.id, name: 'NoSeat' })).statusCode, 403);

  // Assign seats to owner & member
  await ctx.req(owner, 'POST', `/api/accounts/${entId}/apps/${swApp.id}/seats`, {
    subscriptionId: entSub.id, userId: member.user.id,
  });
  await ctx.req(owner, 'PUT', `/api/accounts/${entId}/apps/${swApp.id}/members/${member.user.id}/limit`, {
    cycleLimitUnits: '100000000', // 100 credits limit
  });
  const mKey = (await ctx.req(member, 'POST', '/api/keys', { appId: swApp.id, name: 'MemberKey' })).json().plaintextKey;

  // Concurrent race on member limit (two 60-credit holds against 100-credit limit -> exactly 1 succeeds, 1 hits 409)
  // Using volc:seedream-4.0: baseAmount='0.26087' * 1.15 * 200 = ~60.0001 credits
  const [raceA, raceB] = await Promise.all([
    ctx.gw('POST', '/internal/billing/v1/holds', {
      apiKey: mKey, requestId: 'race-1', routeId: 'volc:seedream-4.0',
      meterVersion: 'm_volc_seedream_v1', routerRateVersion: 'r_volc_v1',
      baseAmount: '0.26', providerCurrency: 'CNY',
    }),
    ctx.gw('POST', '/internal/billing/v1/holds', {
      apiKey: mKey, requestId: 'race-2', routeId: 'volc:seedream-4.0',
      meterVersion: 'm_volc_seedream_v1', routerRateVersion: 'r_volc_v1',
      baseAmount: '0.26', providerCurrency: 'CNY',
    }),
  ]);
  const statuses = [raceA.statusCode, raceB.statusCode].sort();
  assert.deepEqual(statuses, [201, 409]);

  const winnerHold = raceA.statusCode === 201 ? raceA.json().hold : raceB.json().hold;
  // Extend winner hold by +0.10 baseAmount (~23 credits, total ~82.8 <= 100)
  const extRes = await ctx.gw('POST', `/internal/billing/v1/holds/${winnerHold.id}/extend`, {
    apiKey: mKey, additionalBaseAmount: '0.10',
  });
  assert.equal(extRes.statusCode, 200, extRes.body);
  await ctx.gw('POST', `/internal/billing/v1/holds/${winnerHold.id}/commit`, {
    executorId: 'gw-1', attempt: 1, upstreamRef: 'up-ent-1',
  });

  // Revoke member seat -> new holds & catalog fail immediately, yet in-flight winnerHold settles via service token!
  await ctx.req(owner, 'DELETE', `/api/accounts/${entId}/apps/${swApp.id}/seats/${member.user.id}?subscriptionId=${entSub.id}`, {});
  assert.equal((await ctx.gw('POST', '/internal/billing/v1/catalog', { apiKey: mKey })).statusCode, 403);
  const settleWinner = await ctx.gw('POST', `/internal/billing/v1/holds/${winnerHold.id}/settle`, {
    baseAmount: '0.30', providerCurrency: 'CNY',
  });
  assert.equal(settleWinner.statusCode, 200);
  assert.equal(settleWinner.json().hold.status, 'settled');

  // Re-assign seat & verify TTL sweeper (reserved auto-release & reconciling deadline timeout)
  await ctx.req(owner, 'POST', `/api/accounts/${entId}/apps/${swApp.id}/seats`, {
    subscriptionId: entSub.id, userId: member.user.id,
  });
  const hUncommitted = (await ctx.gw('POST', '/internal/billing/v1/holds', {
    apiKey: mKey, requestId: 'ttl-uncommitted', routeId: 'baidu:paddleocr-vl',
    meterVersion: 'm_baidu_ocr_vl_v1', routerRateVersion: 'r_baidu_v1',
    baseAmount: '0.03', providerCurrency: 'CNY', ttlSeconds: 60, reconcileTtlSeconds: 600,
  })).json().hold;
  const hLostTask = (await ctx.gw('POST', '/internal/billing/v1/holds', {
    apiKey: mKey, requestId: 'ttl-lost', routeId: 'baidu:paddleocr-vl',
    meterVersion: 'm_baidu_ocr_vl_v1', routerRateVersion: 'r_baidu_v1',
    baseAmount: '0.03', providerCurrency: 'CNY', ttlSeconds: 60, reconcileTtlSeconds: 180,
  })).json().hold;
  await ctx.gw('POST', `/internal/billing/v1/holds/${hLostTask.id}/commit`, {
    executorId: 'gw-1', attempt: 1, upstreamRef: 'ocr-task-999',
  });

  const tPlus90 = new Date(Date.now() + 90_000);
  const sweep1 = await reconcileExpiredHolds(ctx.pool, {
    now: tPlus90,
    evidenceLookup: async () => null, // still unknown -> extend to reconciling!
  });
  assert.ok(sweep1.autoReleased.includes(hUncommitted.id));
  assert.ok(sweep1.extendedToReconciling.includes(hLostTask.id));

  const tPastDeadline = new Date(Date.now() + 300_000);
  const sweep2 = await reconcileExpiredHolds(ctx.pool, {
    now: tPastDeadline,
    evidenceLookup: async () => ({ verifiedUnits: '2400000' }), // only part verified at deadline
  });
  assert.ok(sweep2.timedOutDifferences.includes(hLostTask.id));

  // Final reconciliation conservation passes
  assert.equal((await ctx.req(operator, 'GET', '/api/admin/billing/reconciliation')).json().ok, true);
});
