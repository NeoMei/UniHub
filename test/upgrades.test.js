import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync, randomBytes, sign } from 'node:crypto';
import { createServer } from 'node:http';
import { test } from 'node:test';
import pg from 'pg';
import { buildApp } from '../src/app.js';
import { computeUpgradeProration } from '../src/billing/upgrade.js';
import { computeSeatExpansionProration } from '../src/billing/subscriptions.js';
import { seedDocFlow } from '../src/seed/docflow.js';
import { seedSuperWagie } from '../src/seed/superwagie.js';

async function testDatabase(t) {
  const connectionString = process.env.UNIHUB_TEST_DATABASE_URL;
  assert.ok(connectionString, 'set UNIHUB_TEST_DATABASE_URL to a dedicated PostgreSQL test database');
  const control = new pg.Pool({ connectionString });
  const schema = `unihub_2c_${randomBytes(8).toString('hex')}`;
  await control.query(`CREATE SCHEMA ${schema}`);
  const pool = new pg.Pool({ connectionString, options: `-c search_path=${schema}` });
  t.after(async () => {
    await pool.end();
    try { await control.query(`DROP SCHEMA ${schema} CASCADE`); }
    finally { await control.end(); }
  });
  return pool;
}

async function fixture(t, { region = 'cn', currency = 'CNY' } = {}) {
  const pool = await testDatabase(t);
  const clientId = 'unihub-2c-test';
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
    pool, region, currency, operatorSubjects: new Set(['operator']),
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

  async function topUp(session, amountMinor, tradeNo) {
    const pay = await req(session, 'POST', '/api/wallet/recharges', { amountMinor });
    assert.equal(pay.statusCode, 201, pay.body);
    const settle = await req(session, 'POST', `/api/wallet/payment-orders/${pay.json().paymentOrder.id}/sandbox-settle`, {
      providerTradeNo: tradeNo,
    });
    assert.equal(settle.statusCode, 200, settle.body);
    return settle.json().wallet;
  }

  return { app, pool, signIn, req, topUp };
}

test('Stage 2c (2c-1): 升级折算 3 组精算用例 (¥0 / ¥588 / 连续 A→B→C ¥99 & ¥149.25) 与在途预扣自动补返', async (t) => {
  // 1. Exact pure integer proration checks matching docs/architecture.md
  assert.equal(computeUpgradeProration({
    paidMinor: 9900n, refundedMinor: 0n, months: 1, currentCycleIndex: 0,
    baseGrantedUnits: 2000_000_000n, baseConsumedUnits: 2000_000_000n,
  }).returnMinor, 0n);

  assert.equal(computeUpgradeProration({
    paidMinor: 67200n, refundedMinor: 0n, months: 12, currentCycleIndex: 1,
    baseGrantedUnits: 2000_000_000n, baseConsumedUnits: 1000_000_000n,
  }).returnMinor, 58800n);

  assert.equal(computeUpgradeProration({
    paidMinor: 19900n, refundedMinor: 0n, months: 1, currentCycleIndex: 0,
    baseGrantedUnits: 5000_000_000n, baseConsumedUnits: 1250_000_000n,
  }).returnMinor, 14925n);

  const ctx = await fixture(t);
  const operator = await ctx.signIn('operator');
  const { app: swApp } = await seedSuperWagie(ctx.pool, {
    operatorUserId: operator.user.id, region: 'cn', currency: 'CNY',
  });

  // --- Scenario A: ¥99 monthly -> consume all 2000M base -> upgrade Max returns ¥0 ---
  const buyerA = await ctx.signIn('buyer-case-a');
  await ctx.topUp(buyerA, 9900 + 19900, 'topup-a');
  await ctx.req(buyerA, 'POST', '/api/billing/checkout', { appId: swApp.id, tierId: 'pro_monthly', quantity: 1 });
  await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});

  const hA = await ctx.req(buyerA, 'POST', '/api/billing/credits/holds', {
    appId: swApp.id, requestId: 'a-use-2000', authorizedUnits: '2000000000', pricingSnapshot: { model: 'm' },
  });
  await ctx.req(operator, 'POST', `/api/billing/credits/holds/${hA.json().hold.id}/settle`, { settledUnits: '2000000000' });

  const upgA = await ctx.req(buyerA, 'POST', '/api/billing/upgrades', { appId: swApp.id, targetTierId: 'max_monthly' });
  assert.equal(upgA.statusCode, 201, upgA.body);
  assert.equal(upgA.json().returnedMinor, '0');
  assert.equal(upgA.json().finalized, true);

  // --- Scenario B: ¥672 yearly -> advance 1 month, consume 1000M base -> upgrade Max returns ¥588 (58800分) ---
  const buyerB = await ctx.signIn('buyer-case-b');
  await ctx.topUp(buyerB, 67200 + 19900, 'topup-b');
  await ctx.req(buyerB, 'POST', '/api/billing/checkout', { appId: swApp.id, tierId: 'pro_yearly', quantity: 1 });
  await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});

  // Advance clock by 32 days past period_start so renewal processor grants cycle 1 (month 2)
  await ctx.pool.query(
    "UPDATE app_subscriptions SET period_start = period_start - interval '32 days' WHERE account_id = $1",
    [buyerB.activeAccountId],
  );
  // Mark cycle 0 expired so cycle 1 base batch is used
  await ctx.pool.query(
    "UPDATE credit_batches SET expires_at = statement_timestamp() - interval '1 hour' WHERE account_id = $1 AND grant_cycle_index = 0",
    [buyerB.activeAccountId],
  );
  const grantRes = await ctx.req(operator, 'POST', '/api/internal/billing/renewals/process', {});
  assert.equal(grantRes.statusCode, 200, grantRes.body);
  assert.equal(grantRes.json().monthlyGrants.length, 1);

  const hB = await ctx.req(buyerB, 'POST', '/api/billing/credits/holds', {
    appId: swApp.id, requestId: 'b-use-1000', authorizedUnits: '1000000000', pricingSnapshot: { model: 'm' },
  });
  assert.equal(hB.statusCode, 201, hB.body);
  await ctx.req(operator, 'POST', `/api/billing/credits/holds/${hB.json().hold.id}/settle`, { settledUnits: '1000000000' });

  const upgB = await ctx.req(buyerB, 'POST', '/api/billing/upgrades', { appId: swApp.id, targetTierId: 'max_monthly' });
  assert.equal(upgB.statusCode, 201, upgB.body);
  assert.equal(upgB.json().returnedMinor, '58800');
  assert.equal(upgB.json().wallet.availableMinor, '58800');

  // --- Scenario C: Continuous A(¥99) + sooner-expiring credit_pack -> B(¥199) -> C(¥399) ---
  const buyerC = await ctx.signIn('buyer-case-c');
  await ctx.topUp(buyerC, 9900 + 3900 + 19900 + 39900, 'topup-c');
  await ctx.req(buyerC, 'POST', '/api/billing/checkout', { appId: swApp.id, tierId: 'pro_monthly', quantity: 1 });
  await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});
  await ctx.req(buyerC, 'POST', '/api/billing/checkout', { appId: swApp.id, tierId: 'pack_1000', quantity: 1 });
  await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});

  // Set credit_pack to expire in 10 days (sooner than monthly plan's 30 days)
  await ctx.pool.query(
    "UPDATE credit_batches SET expires_at = statement_timestamp() + interval '10 days' WHERE account_id = $1 AND source_type = 'credit_pack'",
    [buyerC.activeAccountId],
  );
  const hPack = await ctx.req(buyerC, 'POST', '/api/billing/credits/holds', {
    appId: swApp.id, requestId: 'c-use-pack-800', authorizedUnits: '800000000', pricingSnapshot: { model: 'm' },
  });
  await ctx.req(operator, 'POST', `/api/billing/credits/holds/${hPack.json().hold.id}/settle`, { settledUnits: '800000000' });

  // Upgrade A -> B: A base untouched (sooner pack consumed first!), returns full ¥99 (9900) and keeps remaining 200M pack!
  const upgAB = await ctx.req(buyerC, 'POST', '/api/billing/upgrades', { appId: swApp.id, targetTierId: 'max_monthly' });
  assert.equal(upgAB.statusCode, 201, upgAB.body);
  assert.equal(upgAB.json().returnedMinor, '9900');

  // Consume remaining 200M pack + 1250M of B's 5000M base (= 1450M total), proving pack expired earlier and stays valid across upgrade
  const hBQuarter = await ctx.req(buyerC, 'POST', '/api/billing/credits/holds', {
    appId: swApp.id, requestId: 'c-use-quarter-b', authorizedUnits: '1450000000', pricingSnapshot: { model: 'm' },
  });
  assert.equal(hBQuarter.statusCode, 201, hBQuarter.body);
  await ctx.req(operator, 'POST', `/api/billing/credits/holds/${hBQuarter.json().hold.id}/settle`, { settledUnits: '1450000000' });

  // Upgrade B -> C: B paid ¥199 and used 1/4 of base -> returns ¥149.25 (14925分)
  const upgBC = await ctx.req(buyerC, 'POST', '/api/billing/upgrades', { appId: swApp.id, targetTierId: 'ultra_monthly' });
  assert.equal(upgBC.statusCode, 201, upgBC.body);
  assert.equal(upgBC.json().returnedMinor, '14925');

  // --- Scenario D: Upgrade while hold in-flight -> conservative return first -> automatic post-settle adjustment ---
  const buyerD = await ctx.signIn('buyer-case-d');
  await ctx.topUp(buyerD, 9900 + 19900, 'topup-d');
  await ctx.req(buyerD, 'POST', '/api/billing/checkout', { appId: swApp.id, tierId: 'pro_monthly', quantity: 1 });
  await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});

  const hD = await ctx.req(buyerD, 'POST', '/api/billing/credits/holds', {
    appId: swApp.id, requestId: 'd-inflight-1500', authorizedUnits: '1500000000', pricingSnapshot: { model: 'm' },
  });
  assert.equal(hD.statusCode, 201);

  const upgD = await ctx.req(buyerD, 'POST', '/api/billing/upgrades', { appId: swApp.id, targetTierId: 'max_monthly' });
  assert.equal(upgD.statusCode, 201, upgD.body);
  assert.equal(upgD.json().finalized, false);
  // Conservative return: (9900 * (2000 - 1500)) / 2000 = 2475
  assert.equal(upgD.json().returnedMinor, '2475');

  // Now settle the old in-flight hold for only 500M (1000M released) -> auto adjusts wallet up to R_final = 7425 (+4950)
  const settleD = await ctx.req(operator, 'POST', `/api/billing/credits/holds/${hD.json().hold.id}/settle`, {
    settledUnits: '500000000',
  });
  assert.equal(settleD.statusCode, 200);
  assert.equal((await ctx.req(buyerD, 'GET', '/api/wallet')).json().wallet.availableMinor, '7425');

  const { rows: [upgRowD] } = await ctx.pool.query(
    'SELECT returned_so_far_minor, finalized_at FROM subscription_upgrades WHERE id = $1', [upgD.json().upgradeId],
  );
  assert.equal(upgRowD.returned_so_far_minor, '7425');
  assert.ok(upgRowD.finalized_at);

  const recon = await ctx.req(operator, 'GET', '/api/admin/billing/reconciliation');
  assert.equal(recon.json().ok, true);
});

test('Stage 2c (2c-2 & 2c-3): 企业增席补差、预约降档/减席与取消、3日续费提醒与涨价停扣重授权、DocFlow AI 纯配置复用', async (t) => {
  // Verify exact integer math for mid-cycle seat expansion
  const seatCalc = computeSeatExpansionProration({
    deltaSeats: 2,
    unitMonthMinor: 14900n,
    baseCreditsPerSeat: 3000,
    bonusCreditsPerSeat: 500,
    tCur: 1000n,
    tRem: 500n,
    mFuture: 0n,
  });
  assert.equal(seatCalc.deltaMinor, 14900n); // 2 * 14900 * 0.5
  assert.equal(seatCalc.baseUnits, 3000_000_000n);
  assert.equal(seatCalc.bonusUnits, 500_000_000n);

  const ctx = await fixture(t);
  const operator = await ctx.signIn('operator');
  await seedSuperWagie(ctx.pool, { operatorUserId: operator.user.id, region: 'cn', currency: 'CNY' });
  const { app: dfApp } = await seedDocFlow(ctx.pool, { operatorUserId: operator.user.id, region: 'cn', currency: 'CNY' });

  // 2c-3: DocFlow AI enterprise & personal flows run purely from declarative seed config
  const entOwner = await ctx.signIn('df-ent-owner');
  const entId = (await ctx.req(entOwner, 'POST', '/api/accounts/enterprises', { name: 'DocFlow Corp' })).json().accountId;
  await ctx.req(entOwner, 'POST', '/api/session/account', { accountId: entId });
  await ctx.topUp(entOwner, 200000, 'df-ent-topup');

  // Subscribe 2 enterprise seats on DocFlow (¥119/seat = 23800)
  const subEntRes = await ctx.req(entOwner, 'POST', '/api/billing/checkout', {
    appId: dfApp.id, tierId: 'ent_seat_monthly', quantity: 2, autoRenew: true,
  });
  assert.equal(subEntRes.statusCode, 201, subEntRes.body);
  await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});

  const entSub = (await ctx.req(entOwner, 'GET', '/api/billing/subscriptions')).json().subscriptions[0];
  assert.equal(entSub.seatCount, 2);
  assert.equal(entSub.autoRenew, true);

  // Quote & execute +2 seat expansion -> wallet deducted & enterprise credit pool increased proportionally
  const quoteRes = await ctx.req(entOwner, 'POST', `/api/billing/subscriptions/${entSub.id}/seats/expand-quote`, {
    deltaSeats: 2,
  });
  assert.equal(quoteRes.statusCode, 200, quoteRes.body);
  assert.ok(BigInt(quoteRes.json().deltaMinor) > 0n);

  const expRes = await ctx.req(entOwner, 'POST', `/api/billing/subscriptions/${entSub.id}/seats/expand`, {
    deltaSeats: 2,
  });
  assert.equal(expRes.statusCode, 201, expRes.body);
  assert.equal(expRes.json().subscription.seatCount, 4);
  assert.ok(BigInt(expRes.json().grantedBaseUnits) > 0n);

  // Schedule seat reduction to 2 seats at period_end, then cancel it
  const schedRes = await ctx.req(entOwner, 'PUT', `/api/billing/subscriptions/${entSub.id}/scheduled-change`, {
    targetTierId: 'ent_seat_monthly', targetSeatCount: 2,
  });
  assert.equal(schedRes.statusCode, 200);
  assert.equal(schedRes.json().subscription.scheduledChange.targetSeatCount, 2);

  const cancelSched = await ctx.req(entOwner, 'DELETE', `/api/billing/subscriptions/${entSub.id}/scheduled-change`, {});
  assert.equal(cancelSched.statusCode, 200);
  assert.equal(cancelSched.json().subscription.scheduledChange, null);

  // Personal DocFlow subscription: OCR pack expires sooner & is consumed first + upgrade + renewal price-increase protection
  const dfUser = await ctx.signIn('df-personal');
  await ctx.topUp(dfUser, 5900 + 2200 + 12900 + 50000, 'df-personal-topup');
  await ctx.req(dfUser, 'POST', '/api/billing/checkout', {
    appId: dfApp.id, tierId: 'standard_monthly', quantity: 1, autoRenew: true,
  });
  await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});
  await ctx.req(dfUser, 'POST', '/api/billing/checkout', {
    appId: dfApp.id, tierId: 'ocr_pack', quantity: 1,
  });
  await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});

  // Set ocr_pack expiry to 20 days -> consumes from pack before monthly plan
  await ctx.pool.query(
    "UPDATE credit_batches SET expires_at = statement_timestamp() + interval '20 days' WHERE account_id = $1 AND source_type = 'credit_pack'",
    [dfUser.activeAccountId],
  );
  const dfHold = await ctx.req(dfUser, 'POST', '/api/billing/credits/holds', {
    appId: dfApp.id, requestId: 'df-ocr-use-300', authorizedUnits: '300000000', pricingSnapshot: { model: 'paddleocr-vl' },
  });
  await ctx.req(operator, 'POST', `/api/billing/credits/holds/${dfHold.json().hold.id}/settle`, { settledUnits: '300000000' });

  // Upgrade standard_monthly (¥59) -> pro_monthly (¥129) with autoRenew=true -> returns full ¥59!
  const dfUpg = await ctx.req(dfUser, 'POST', '/api/billing/upgrades', {
    appId: dfApp.id, targetTierId: 'pro_monthly', autoRenew: true,
  });
  assert.equal(dfUpg.statusCode, 201);
  assert.equal(dfUpg.json().returnedMinor, '5900');

  const proSub = dfUpg.json().subscription;

  // Test 3-day upcoming renewal notice
  const nearEnd = new Date(new Date(proSub.periodEnd).getTime() - 2 * 86_400_000).toISOString();
  const noticeRun = await ctx.req(operator, 'POST', '/api/internal/billing/renewals/process', { now: nearEnd });
  assert.equal(noticeRun.statusCode, 200);
  assert.ok(noticeRun.json().noticesSent.includes(proSub.id));

  // Publish higher price for pro_monthly (¥129 -> ¥159) effective before period_end -> auto-renew halts & requests re-auth!
  const cardsRes = await ctx.req(operator, 'GET', `/api/admin/apps/${dfApp.id}/product-drafts?kind=personal`);
  const proCard = cardsRes.json().cards.find((c) => c.tierId === 'pro_monthly');
  await ctx.req(operator, 'PUT', `/api/admin/apps/${dfApp.id}/product-drafts/${proCard.id}`, {
    accountKind: 'personal',
    productType: 'membership',
    tierId: 'pro_monthly',
    tierRank: 30,
    rule: { billingCycle: 'monthly', priceMinor: 15900, baseCredits: 3200, bonusCredits: 600 },
    display: proCard.display,
  });
  const pubNewPrice = await ctx.req(operator, 'POST', `/api/admin/apps/${dfApp.id}/product-drafts/${proCard.id}/publish`, {
    effectiveAt: nearEnd,
  });
  assert.equal(pubNewPrice.statusCode, 201, pubNewPrice.body);

  const walletBeforeRenew = (await ctx.req(dfUser, 'GET', '/api/wallet')).json().wallet.availableMinor;
  const dueTime = new Date(new Date(proSub.periodEnd).getTime() + 60_000).toISOString();
  const priceGuardRun = await ctx.req(operator, 'POST', '/api/internal/billing/renewals/process', { now: dueTime });
  assert.equal(priceGuardRun.statusCode, 200);
  assert.equal(priceGuardRun.json().haltedPriceIncrease.length, 1);
  assert.equal(priceGuardRun.json().haltedPriceIncrease[0].subscriptionId, proSub.id);
  // Zero charge on price increase
  assert.equal((await ctx.req(dfUser, 'GET', '/api/wallet')).json().wallet.availableMinor, walletBeforeRenew);

  // Reconciliation remains 100% conserved
  assert.equal((await ctx.req(operator, 'GET', '/api/admin/billing/reconciliation')).json().ok, true);
});
