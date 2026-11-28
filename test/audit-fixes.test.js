import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync, randomBytes, sign } from 'node:crypto';
import { createServer } from 'node:http';
import { test } from 'node:test';
import pg from 'pg';
import { buildApp } from '../src/app.js';
import { expireCreditBatches } from '../src/billing/credits.js';
import { runReconciliation } from '../src/billing/reconciliation.js';
import { seedSuperWagie } from '../src/seed/superwagie.js';

async function testDatabase(t) {
  const connectionString = process.env.UNIHUB_TEST_DATABASE_URL;
  assert.ok(connectionString, 'set UNIHUB_TEST_DATABASE_URL to a dedicated PostgreSQL test database');
  const control = new pg.Pool({ connectionString });
  const schema = `unihub_audit_${randomBytes(8).toString('hex')}`;
  await control.query(`CREATE SCHEMA ${schema}`);
  const pool = new pg.Pool({ connectionString, options: `-c search_path=${schema}` });
  t.after(async () => {
    await pool.end();
    try { await control.query(`DROP SCHEMA ${schema} CASCADE`); }
    finally { await control.end(); }
  });
  return pool;
}

async function setupEnv(t, { allowSandboxSettle = true, lagoHandler = null } = {}) {
  const pool = await testDatabase(t);
  const { privateKey, publicKey } = generateKeyPairSync('ec', { namedCurve: 'secp384r1' });
  const clientId = 'audit-client';
  let subject = 'u';
  let attempt;
  let issuer;

  const oidcSrv = createServer(async (request, response) => {
    const send = (status, value) => {
      response.writeHead(status, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify(value));
    };
    if (request.url === '/oidc/.well-known/openid-configuration') {
      return send(200, {
        issuer,
        authorization_endpoint: `${issuer}/auth`,
        token_endpoint: `${issuer}/token`,
        end_session_endpoint: `${issuer}/session/end`,
        jwks_uri: `${issuer}/jwks`,
        id_token_signing_alg_values_supported: ['ES384'],
        code_challenge_methods_supported: ['S256'],
      });
    }
    if (request.url === '/oidc/jwks') {
      return send(200, { keys: [{ ...publicKey.export({ format: 'jwk' }), kid: 'k', alg: 'ES384' }] });
    }
    if (request.url === '/oidc/token' && request.method === 'POST') {
      const chunks = [];
      for await (const c of request) chunks.push(c);
      const body = new URLSearchParams(Buffer.concat(chunks).toString());
      const challenge = createHash('sha256').update(body.get('code_verifier') ?? '').digest('base64url');
      if (body.get('code') !== 'valid' || challenge !== attempt?.searchParams.get('code_challenge')) {
        return send(400, { error: 'invalid_grant' });
      }
      const now = Math.floor(Date.now() / 1000);
      const payload = {
        iss: issuer, aud: clientId, sub: subject, iat: now, exp: now + 600,
        nonce: attempt.searchParams.get('nonce'), name: subject, email: `${subject}@example.test`,
      };
      const unsigned = [{ alg: 'ES384', kid: 'k' }, payload].map((x) => Buffer.from(JSON.stringify(x)).toString('base64url')).join('.');
      const sig = sign('sha384', Buffer.from(unsigned), { key: privateKey, dsaEncoding: 'ieee-p1363' });
      return send(200, { token_type: 'Bearer', access_token: 'tok', id_token: `${unsigned}.${sig.toString('base64url')}` });
    }
    return send(404, {});
  });
  await new Promise((resolve) => oidcSrv.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => oidcSrv.close(resolve)));
  issuer = `http://127.0.0.1:${oidcSrv.address().port}/oidc`;

  let lagoConfig = {};
  if (lagoHandler) {
    const lagoSrv = createServer(async (request, response) => {
      const chunks = [];
      for await (const c of request) chunks.push(c);
      const raw = Buffer.concat(chunks).toString('utf8');
      const out = await lagoHandler({ method: request.method, url: request.url, body: raw ? JSON.parse(raw) : {} });
      response.writeHead(out.status ?? 200, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify(out.body ?? {}));
    });
    await new Promise((resolve) => lagoSrv.listen(0, '127.0.0.1', resolve));
    t.after(() => new Promise((resolve) => lagoSrv.close(resolve)));
    lagoConfig = { enabled: true, baseUrl: `http://127.0.0.1:${lagoSrv.address().port}`, apiKey: 'lago-k' };
  }

  const app = await buildApp({
    origin: new URL('http://127.0.0.1:14000'),
    issuer: new URL(issuer),
    clientId,
    clientSecret: 'secret',
    sessionKey: randomBytes(32),
    pool,
    region: 'cn',
    currency: 'CNY',
    operatorSubjects: new Set(['op']),
    paymentConfig: { allowSandboxSettle, allowUnsignedTestWebhooks: true },
    lagoConfig,
  });
  t.after(() => app.close());

  async function signIn(sub) {
    subject = sub;
    const login = await app.inject('/login');
    attempt = new URL(login.headers.location);
    const loginCookie = login.cookies[0];
    const cb = await app.inject({
      url: `/auth/callback?code=valid&state=${encodeURIComponent(attempt.searchParams.get('state'))}`,
      cookies: { [loginCookie.name]: loginCookie.value },
    });
    const sessionCookie = cb.cookies[0];
    const cookies = { [sessionCookie.name]: sessionCookie.value };
    const sess = await app.inject({ url: '/api/session', cookies });
    return { cookies, ...sess.json() };
  }

  function req(session, method, url, payload) {
    return app.inject({
      method, url, cookies: session.cookies, payload,
      headers: method === 'GET' ? {} : { 'x-csrf-token': session.csrf },
    });
  }

  return { app, pool, signIn, req };
}

test('Audit fixes: hold IDOR protection, 0-unit settle, repeated batch expiration & allowSandboxSettle session flag', async (t) => {
  const ctx = await setupEnv(t, { allowSandboxSettle: false });
  const op = await ctx.signIn('op');
  const alice = await ctx.signIn('alice');
  const mallory = await ctx.signIn('mallory');

  assert.equal(alice.allowSandboxSettle, false);
  const { app: swApp } = await seedSuperWagie(ctx.pool, { operatorUserId: op.user.id, region: 'cn', currency: 'CNY' });

  // Recharge alice via webhook (sandbox disabled) and buy pro_monthly
  const pOrder = (await ctx.req(alice, 'POST', '/api/wallet/recharges', { provider: 'wechat', amountMinor: 20000 })).json().paymentOrder;
  assert.equal((await ctx.req(alice, 'POST', `/api/wallet/payment-orders/${pOrder.id}/sandbox-settle`, { providerTradeNo: 'x' })).statusCode, 403);
  await ctx.app.inject({
    method: 'POST', url: '/api/webhooks/payments/wechat',
    payload: { paymentOrderId: pOrder.id, merchantId: 'cn-default-merchant', providerTradeNo: 'wx-a1', amountMinor: 20000, region: 'cn', currency: 'CNY' },
  });
  await ctx.req(alice, 'POST', '/api/billing/checkout', { appId: swApp.id, tierId: 'pro_monthly', orderType: 'subscribe', quantity: 1 });
  await ctx.req(op, 'POST', '/api/internal/billing/outbox/process', {});

  // Create hold for alice -> mallory CANNOT settle or release it (403 IDOR block)
  const holdRes = await ctx.req(alice, 'POST', '/api/billing/credits/holds', {
    appId: swApp.id, requestId: 'alice-h1', authorizedUnits: '500000', pricingSnapshot: { model: 'x' },
  });
  assert.equal(holdRes.statusCode, 201);
  const holdId = holdRes.json().hold.id;

  const mallorySettle = await ctx.req(mallory, 'POST', `/api/billing/credits/holds/${holdId}/settle`, { settledUnits: '100000' });
  assert.equal(mallorySettle.statusCode, 403);
  assert.equal((await ctx.req(mallory, 'POST', `/api/billing/credits/holds/${holdId}/release`, {})).statusCode, 403);

  // Settling 0 units succeeds and releases back to available
  const zeroSettle = await ctx.req(alice, 'POST', `/api/billing/credits/holds/${holdId}/settle`, { settledUnits: '0' });
  assert.equal(zeroSettle.statusCode, 200, zeroSettle.body);
  assert.equal(zeroSettle.json().hold.status, 'settled');
  assert.equal(zeroSettle.json().hold.settledUnits, '0');
  assert.equal(zeroSettle.json().hold.releasedUnits, '500000');

  // Create second hold, expire batch, then release second hold onto expired batch & expire again
  const hold2 = (await ctx.req(alice, 'POST', '/api/billing/credits/holds', {
    appId: swApp.id, requestId: 'alice-h2', authorizedUnits: '1000000', pricingSnapshot: { model: 'x' },
  })).json().hold;
  const farFuture = new Date(Date.now() + 90 * 86_400_000);
  const exp1 = await expireCreditBatches(ctx.pool, { now: farFuture });
  assert.ok(exp1.expiredBatches >= 1);

  assert.equal((await ctx.req(alice, 'POST', `/api/billing/credits/holds/${hold2.id}/release`, {})).statusCode, 200);
  const exp2 = await expireCreditBatches(ctx.pool, { now: farFuture });
  assert.ok(exp2.expiredBatches >= 1);

  const recon = await runReconciliation(ctx.pool, { region: 'cn', currency: 'CNY' });
  assert.equal(recon.ok, true);
  assert.deepEqual(recon.batchMismatches, []);
});

test('Audit fixes: enterprise canRecharge guard, pendingCheckout cleanup, upgrade/expansion Lago sync & failed list', async (t) => {
  let failLago = false;
  const invoicedOrders = [];
  const ctx = await setupEnv(t, {
    lagoHandler: async ({ method, url, body }) => {
      if (failLago) return { status: 503, body: { error: 'down' } };
      if (url === '/api/v1/customers' && method === 'POST') return { status: 200, body: { customer: body.customer } };
      if (url === '/api/v1/invoices' && method === 'POST') {
        invoicedOrders.push(body.invoice.fees[0].description);
        return { status: 200, body: { invoice: { lago_id: `inv-${invoicedOrders.length}` } } };
      }
      if (url.startsWith('/api/v1/invoices/') && method === 'PUT') return { status: 200, body: { invoice: { payment_status: 'succeeded' } } };
      return { status: 404 };
    },
  });

  const op = await ctx.signIn('op');
  const owner = await ctx.signIn('ent-owner');
  const purchaser = await ctx.signIn('purchaser');
  const { app: swApp } = await seedSuperWagie(ctx.pool, { operatorUserId: op.user.id, region: 'cn', currency: 'CNY' });

  // Enterprise member with canPurchase=true, canRecharge=false hits insufficient_funds -> no payment order created
  const entId = (await ctx.req(owner, 'POST', '/api/accounts/enterprises', { name: 'AuditEnt' })).json().accountId;
  await ctx.req(owner, 'PUT', `/api/accounts/${entId}/members/${purchaser.user.id}`, {
    role: 'admin', canRecharge: false, canPurchase: true, hasSeat: true,
  });
  await ctx.req(purchaser, 'POST', '/api/session/account', { accountId: entId });

  const noRecCheckout = await ctx.req(purchaser, 'POST', '/api/billing/checkout', {
    appId: swApp.id, tierId: 'ent_monthly', orderType: 'subscribe', quantity: 1,
  });
  assert.equal(noRecCheckout.statusCode, 402);
  assert.equal(noRecCheckout.json().canRecharge, false);
  assert.equal(noRecCheckout.json().paymentOrder, undefined);
  assert.equal((await ctx.req(purchaser, 'GET', '/api/wallet/payment-orders')).json().paymentOrders.length, 0);

  // Personal user pendingCheckout dismiss + auto-clear + upgrade Lago sync
  const po402 = (await ctx.req(owner, 'POST', '/api/billing/checkout', {
    appId: swApp.id, tierId: 'pro_monthly', orderType: 'subscribe', quantity: 1,
  })).json();
  assert.equal(po402.canRecharge, true);
  assert.ok(po402.paymentOrder.id);

  await ctx.req(owner, 'POST', `/api/wallet/payment-orders/${po402.paymentOrder.id}/sandbox-settle`, { providerTradeNo: 'sb-p1' });
  assert.equal((await ctx.req(owner, 'DELETE', `/api/wallet/payment-orders/${po402.paymentOrder.id}/pending-checkout`, {})).statusCode, 204);
  const afterDismiss = (await ctx.req(owner, 'GET', '/api/wallet/payment-orders')).json().paymentOrders[0];
  assert.equal(afterDismiss.pendingCheckout, null);

  // Top up more, buy pro_monthly then upgrade while Lago is temporarily failing -> upgrade order shows up in /api/admin/lago/failed
  const po2 = (await ctx.req(owner, 'POST', '/api/wallet/recharges', { provider: 'wechat', amountMinor: 40000 })).json().paymentOrder;
  await ctx.req(owner, 'POST', `/api/wallet/payment-orders/${po2.id}/sandbox-settle`, { providerTradeNo: 'sb-p2' });
  await ctx.req(owner, 'POST', '/api/billing/checkout', { appId: swApp.id, tierId: 'pro_monthly', orderType: 'subscribe', quantity: 1 });
  await ctx.req(op, 'POST', '/api/internal/billing/outbox/process', {});

  failLago = true;
  const upgRes = await ctx.req(owner, 'POST', '/api/billing/upgrades', { appId: swApp.id, targetTierId: 'max_monthly' });
  assert.equal(upgRes.statusCode, 201);
  assert.equal(upgRes.json().order.lagoSyncStatus, 'pending');
  await ctx.req(op, 'POST', '/api/internal/billing/outbox/process', {});

  const failedRes = await ctx.req(op, 'GET', '/api/admin/lago/failed');
  assert.equal(failedRes.statusCode, 200);
  assert.equal(failedRes.json().orders.length, 1);
  assert.equal(failedRes.json().orders[0].id, upgRes.json().order.id);

  // Retry succeeds when Lago recovers
  failLago = false;
  assert.equal((await ctx.req(op, 'POST', `/api/admin/lago/retry/${upgRes.json().order.id}`, {})).statusCode, 200);
  assert.equal((await ctx.req(op, 'GET', '/api/admin/lago/failed')).json().orders.length, 0);
});
