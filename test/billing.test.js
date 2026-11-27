import assert from 'node:assert/strict';
import {
  createCipheriv,
  createHash,
  createHmac,
  createSign,
  generateKeyPairSync,
  randomBytes,
  randomUUID,
  sign,
} from 'node:crypto';
import { createServer } from 'node:http';
import { test } from 'node:test';
import pg from 'pg';
import { buildApp } from '../src/app.js';
import { buildAlipaySignString } from '../src/payments/verifiers.js';

async function testDatabase(t) {
  const connectionString = process.env.UNIHUB_TEST_DATABASE_URL;
  assert.ok(connectionString, 'set UNIHUB_TEST_DATABASE_URL to a dedicated PostgreSQL test database');
  const control = new pg.Pool({ connectionString });
  const schema = `unihub_2b_${randomBytes(8).toString('hex')}`;
  await control.query(`CREATE SCHEMA ${schema}`);
  const pool = new pg.Pool({ connectionString, options: `-c search_path=${schema}` });
  t.after(async () => {
    await pool.end();
    try { await control.query(`DROP SCHEMA ${schema} CASCADE`); }
    finally { await control.end(); }
  });
  return pool;
}

async function fixture(t, { region = 'cn', currency = 'CNY', paymentConfig = {} } = {}) {
  const pool = await testDatabase(t);
  const clientId = 'unihub-2b-test';
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
    paymentConfig,
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

  return { app, pool, signIn, req };
}

async function seedMembershipProduct(req, operator, appId, {
  accountKind = 'personal', tierId = 'pro_monthly', priceMinor = 9900,
  billingCycle = 'monthly', baseCredits = 2000, bonusCredits = 500,
} = {}) {
  const draft = await req(operator, 'POST', `/api/admin/apps/${appId}/product-drafts`, {
    accountKind,
    productType: 'membership',
    tierId,
    tierRank: 20,
    rule: { billingCycle, priceMinor, baseCredits, bonusCredits },
    display: { name: tierId, description: 'Membership plan', benefits: ['Unified API access'] },
  });
  assert.equal(draft.statusCode, 201, draft.body);
  const pub = await req(operator, 'POST', `/api/admin/apps/${appId}/product-drafts/${draft.json().card.id}/publish`, {
    changeSummary: 'Publish product',
  });
  assert.equal(pub.statusCode, 201, pub.body);
  return pub.json().version;
}

function buildWechatSignedRequest(payloadObj, { serial, rsaPrivateKey, apiV3Key }) {
  const nonce = randomBytes(12).toString('hex');
  const associatedData = 'transaction';
  const iv = Buffer.from(nonce.slice(0, 12), 'utf8');
  const cipher = createCipheriv('aes-256-gcm', Buffer.from(apiV3Key, 'utf8'), iv);
  cipher.setAAD(Buffer.from(associatedData, 'utf8'));
  const encrypted = Buffer.concat([cipher.update(JSON.stringify(payloadObj), 'utf8'), cipher.final(), cipher.getAuthTag()]);
  const bodyObj = {
    id: randomUUID(),
    event_type: 'TRANSACTION.SUCCESS',
    resource: {
      algorithm: 'AEAD_AES_256_GCM',
      ciphertext: encrypted.toString('base64'),
      nonce: iv.toString('utf8'),
      associated_data: associatedData,
    },
  };
  const rawBody = JSON.stringify(bodyObj);
  const ts = String(Math.floor(Date.now() / 1000));
  const msgNonce = randomBytes(8).toString('hex');
  const signer = createSign('RSA-SHA256');
  signer.update(`${ts}\n${msgNonce}\n${rawBody}\n`, 'utf8');
  signer.end();
  const signature = signer.sign(rsaPrivateKey, 'base64');
  return {
    headers: {
      'content-type': 'application/json',
      'wechatpay-timestamp': ts,
      'wechatpay-nonce': msgNonce,
      'wechatpay-serial': serial,
      'wechatpay-signature': signature,
    },
    payload: rawBody,
  };
}

test('Stage 2b (2b-1 & refund/chargeback): 余额不足保留待购上下文、真验签微信/支付宝/Stripe回调、3次重放幂等与错币拦截', async (t) => {
  const wechatKeys = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const alipayKeys = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const apiV3Key = '0123456789abcdef0123456789abcdef';
  const serial = 'WX-PLATFORM-001';

  const cnCtx = await fixture(t, {
    region: 'cn',
    currency: 'CNY',
    paymentConfig: {
      wechat: {
        platformPublicKeys: { [serial]: wechatKeys.publicKey.export({ type: 'spki', format: 'pem' }) },
        apiV3Key,
      },
      alipay: {
        alipayPublicKey: alipayKeys.publicKey.export({ type: 'spki', format: 'pem' }),
      },
    },
  });

  const operator = await cnCtx.signIn('operator');
  const user = await cnCtx.signIn('buyer-1');
  const createdApp = await cnCtx.req(operator, 'POST', '/api/admin/apps', {
    name: 'SuperWagie', redirectUris: ['https://sw.example.test/cb'],
  });
  const appId = createdApp.json().app.id;
  const version = await seedMembershipProduct(cnCtx.req, operator, appId, { priceMinor: 9900 });

  // 1. Checkout with 0 balance -> 402 preserves pendingCheckout and creates pending paymentOrder
  const lowBalanceRes = await cnCtx.req(user, 'POST', '/api/billing/checkout', {
    appId, tierId: 'pro_monthly', quantity: 1, provider: 'wechat', merchantId: 'mch-cn-01',
    returnUrl: 'https://sw.example.test/billing/done',
  });
  assert.equal(lowBalanceRes.statusCode, 402);
  const lowBody = lowBalanceRes.json();
  assert.equal(lowBody.error, 'insufficient_funds');
  assert.equal(lowBody.amountMinor, '9900');
  assert.equal(lowBody.pendingCheckout.tierId, 'pro_monthly');
  assert.equal(lowBody.pendingCheckout.versionId, version.id);
  assert.equal(lowBody.paymentOrder.status, 'pending');

  // Pending order is visible via GET /api/wallet/payment-orders
  const pendingList = await cnCtx.req(user, 'GET', '/api/wallet/payment-orders');
  assert.equal(pendingList.statusCode, 200);
  assert.equal(pendingList.json().paymentOrders.length, 1);

  const paymentOrderId = lowBody.paymentOrder.id;

  // 2. Reject invalid signature with 401 and zero ledger change
  const badSig = await cnCtx.app.inject({
    method: 'POST',
    url: '/api/webhooks/payments/wechat',
    headers: { 'content-type': 'application/json', 'wechatpay-serial': serial, 'wechatpay-timestamp': String(Math.floor(Date.now() / 1000)), 'wechatpay-nonce': 'n1', 'wechatpay-signature': 'bad==' },
    payload: JSON.stringify({ out_trade_no: paymentOrderId }),
  });
  assert.equal(badSig.statusCode, 401);

  // 3. Reject cross-region / wrong currency (USD on CN server) cleanly with 400 and zero ledger mutation
  const wrongCurrencySigned = buildWechatSignedRequest({
    out_trade_no: paymentOrderId,
    mchid: 'mch-cn-01',
    transaction_id: 'wx-trade-wrong-currency',
    amount: { total: 9900, currency: 'USD' },
  }, { serial, rsaPrivateKey: wechatKeys.privateKey, apiV3Key });
  const wrongCurrencyRes = await cnCtx.app.inject({
    method: 'POST', url: '/api/webhooks/payments/wechat', ...wrongCurrencySigned,
  });
  assert.equal(wrongCurrencyRes.statusCode, 400);
  assert.equal((await cnCtx.req(user, 'GET', '/api/wallet')).json().wallet.availableMinor, '0');

  // 4. Signed WeChat v3 AEAD webhook replayed 3 times -> settles payment and credits wallet ONCE
  const wxSigned = buildWechatSignedRequest({
    out_trade_no: paymentOrderId,
    mchid: 'mch-cn-01',
    transaction_id: 'wx-trade-9900-1',
    amount: { total: 9900, currency: 'CNY' },
  }, { serial, rsaPrivateKey: wechatKeys.privateKey, apiV3Key });

  for (let i = 0; i < 3; i++) {
    const res = await cnCtx.app.inject({ method: 'POST', url: '/api/webhooks/payments/wechat', ...wxSigned });
    assert.equal(res.statusCode, 200, res.body);
    assert.equal(res.json().duplicate, i > 0);
    assert.equal(res.json().wallet.availableMinor, '9900');
  }
  const ledgerAfterWx = await cnCtx.req(user, 'GET', '/api/wallet/ledger');
  assert.equal(ledgerAfterWx.json().entries.length, 1);

  // 5. Return to checkout using preserved pendingCheckout -> succeeds and queues outbox
  const checkoutRes = await cnCtx.req(user, 'POST', '/api/billing/checkout', lowBody.pendingCheckout);
  assert.equal(checkoutRes.statusCode, 201, checkoutRes.body);
  assert.equal(checkoutRes.json().wallet.availableMinor, '0');

  // 6. Alipay RSA2 recharge + partial refund + chargeback arrears freeze + recharge repayment
  const aliOrderRes = await cnCtx.req(user, 'POST', '/api/wallet/recharges', {
    provider: 'alipay', merchantId: 'ali-mch-01', amountMinor: 10000,
  });
  assert.equal(aliOrderRes.statusCode, 201);
  const aliPaymentId = aliOrderRes.json().paymentOrder.id;
  const aliParams = {
    out_trade_no: aliPaymentId,
    seller_id: 'ali-mch-01',
    trade_no: 'ali-trade-001',
    total_amount: '100.00',
    trade_status: 'TRADE_SUCCESS',
    sign_type: 'RSA2',
  };
  const aliSignStr = buildAlipaySignString(aliParams);
  const aliSigner = createSign('RSA-SHA256');
  aliSigner.update(aliSignStr, 'utf8');
  aliSigner.end();
  aliParams.sign = aliSigner.sign(alipayKeys.privateKey, 'base64');

  const aliCb = await cnCtx.app.inject({
    method: 'POST', url: '/api/webhooks/payments/alipay',
    headers: { 'content-type': 'application/json' },
    payload: JSON.stringify(aliParams),
  });
  assert.equal(aliCb.statusCode, 200, aliCb.body);
  assert.equal(aliCb.json().wallet.availableMinor, '10000');

  // Operator refunds 30.00 CNY back to original channel (idempotent)
  const ref1 = await cnCtx.req(operator, 'POST', `/api/admin/billing/payments/${aliPaymentId}/refund`, {
    amountMinor: 3000, idempotencyKey: 'ref-req-1',
  });
  assert.equal(ref1.statusCode, 200, ref1.body);
  assert.equal(ref1.json().wallet.availableMinor, '7000');
  assert.equal(ref1.json().paymentOrder.refundableMinor, '7000');
  const refDup = await cnCtx.req(operator, 'POST', `/api/admin/billing/payments/${aliPaymentId}/refund`, {
    amountMinor: 3000, idempotencyKey: 'ref-req-1',
  });
  assert.equal(refDup.json().duplicate, true);
  assert.equal(refDup.json().wallet.availableMinor, '7000');

  // Chargeback for full 100.00 CNY leaves 0 available + 30.00 CNY arrears and freezes wallet
  const cbRes = await cnCtx.req(operator, 'POST', `/api/admin/billing/payments/${aliPaymentId}/chargeback`, {
    amountMinor: 10000, disputeId: 'disp-1',
  });
  assert.equal(cbRes.statusCode, 200, cbRes.body);
  assert.equal(cbRes.json().wallet.availableMinor, '0');
  assert.equal(cbRes.json().wallet.arrearsMinor, '3000');
  assert.equal(cbRes.json().wallet.frozenReason, 'chargeback_arrears');

  // Process outbox for earlier order, verify frozen wallet rejects new credit hold
  await cnCtx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});
  const frozenHold = await cnCtx.req(user, 'POST', '/api/billing/credits/holds', {
    appId, requestId: 'req-while-frozen', authorizedUnits: '1000000', pricingSnapshot: { model: 'm1' },
  });
  assert.equal(frozenHold.statusCode, 409);
  assert.equal(frozenHold.json().error, 'wallet_frozen');

  // US Stripe HMAC webhook verification test
  const stripeSecret = 'whsec_test_secret_12345';
  const usCtx = await fixture(t, {
    region: 'us',
    currency: 'USD',
    paymentConfig: { stripe: { webhookSecret: stripeSecret } },
  });
  const usUser = await usCtx.signIn('us-buyer');
  const usPay = await usCtx.req(usUser, 'POST', '/api/wallet/recharges', {
    provider: 'stripe', merchantId: 'us-mch-01', amountMinor: 2500,
  });
  assert.equal(usPay.statusCode, 201);
  const stripeRaw = JSON.stringify({
    type: 'checkout.session.completed',
    data: {
      object: {
        id: 'cs_test_001',
        payment_intent: 'pi_test_001',
        amount_total: 2500,
        currency: 'usd',
        metadata: { paymentOrderId: usPay.json().paymentOrder.id, merchantId: 'us-mch-01', region: 'us' },
      },
    },
  });
  const ts = Math.floor(Date.now() / 1000);
  const sigHex = createHmac('sha256', stripeSecret).update(`${ts}.${stripeRaw}`, 'utf8').digest('hex');
  const stripeCb = await usCtx.app.inject({
    method: 'POST', url: '/api/webhooks/payments/stripe',
    headers: { 'content-type': 'application/json', 'stripe-signature': `t=${ts},v1=${sigHex}` },
    payload: stripeRaw,
  });
  assert.equal(stripeCb.statusCode, 200, stripeCb.body);
  assert.equal(stripeCb.json().wallet.availableMinor, '2500');
});

test('Stage 2b (2b-2): 扣款后未发分崩溃恢复(outbox幂等)、账本不可变触发器与资金/积分守恒核对', async (t) => {
  const ctx = await fixture(t, { region: 'cn', currency: 'CNY' });
  const operator = await ctx.signIn('operator');
  const user = await ctx.signIn('personal-2b2');
  const appId = (await ctx.req(operator, 'POST', '/api/admin/apps', {
    name: 'AppCrashTest', redirectUris: ['https://app.example.test/cb'],
  })).json().app.id;
  await seedMembershipProduct(ctx.req, operator, appId, {
    tierId: 'pro_yearly', billingCycle: 'yearly', priceMinor: 67200, baseCredits: 2000, bonusCredits: 500,
  });

  // Recharge via sandbox settle
  const payRes = await ctx.req(user, 'POST', '/api/wallet/recharges', { amountMinor: 67200 });
  const payId = payRes.json().paymentOrder.id;
  await ctx.req(user, 'POST', `/api/wallet/payment-orders/${payId}/sandbox-settle`, {
    providerTradeNo: 'sb-trade-2b2',
  });

  // Checkout commits wallet debit + confirmed order + pending outbox (simulating crash before worker runs)
  const coRes = await ctx.req(user, 'POST', '/api/billing/checkout', {
    appId, tierId: 'pro_yearly', quantity: 1,
  });
  assert.equal(coRes.statusCode, 201);
  assert.equal(coRes.json().order.status, 'confirmed');
  assert.equal((await ctx.req(user, 'GET', `/api/billing/credits?appId=${appId}`)).json().balance.availableUnits, '0');

  // Outbox worker runs twice -> grants once and is idempotent on replay
  const run1 = await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});
  assert.equal(run1.statusCode, 200);
  assert.equal(run1.json().processed, 1);
  const run2 = await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});
  assert.equal(run2.json().processed, 0);

  const credits = (await ctx.req(user, 'GET', `/api/billing/credits?appId=${appId}`)).json().balance;
  assert.equal(credits.grantedUnits, '2500000000'); // (2000 + 500) * 1_000_000
  assert.equal(credits.availableUnits, '2500000000');

  // Verify yearly subscription has period_end ~1 year ahead while cycle 0 batches expire ~1 month ahead
  const subs = (await ctx.req(user, 'GET', '/api/billing/subscriptions')).json().subscriptions;
  assert.equal(subs.length, 1);
  assert.equal(subs[0].billingCycle, 'yearly');
  const { rows: batchRows } = await ctx.pool.query(
    'SELECT source_type, expires_at FROM credit_batches WHERE subscription_id = $1', [subs[0].id],
  );
  assert.equal(batchRows.length, 2);
  for (const row of batchRows) {
    assert.ok(new Date(row.expires_at).getTime() < new Date(subs[0].periodEnd).getTime());
  }

  // Immutable ledger triggers reject UPDATE/DELETE
  await assert.rejects(ctx.pool.query('UPDATE cash_ledger_entries SET delta_minor = 0'), /immutable/i);
  await assert.rejects(ctx.pool.query('DELETE FROM credit_ledger_entries'), /immutable/i);

  // Conservation reconciliation check passes with zero mismatches
  const recon = await ctx.req(operator, 'GET', '/api/admin/billing/reconciliation');
  assert.equal(recon.statusCode, 200);
  assert.equal(recon.json().ok, true);
  assert.deepEqual(recon.json().cashMismatches, []);
  assert.deepEqual(recon.json().batchMismatches, []);
});

test('Stage 2b (2b-3 & 2b-4): 企业2席并发5人防超配、跨应用撤权隔离、成员移除级联与在途按原周期结算、一次性邀请认领', async (t) => {
  const ctx = await fixture(t, { region: 'cn', currency: 'CNY' });
  const operator = await ctx.signIn('operator');
  const owner = await ctx.signIn('ent-owner');
  const u1 = await ctx.signIn('member-1');
  const u2 = await ctx.signIn('member-2');
  const u3 = await ctx.signIn('member-3');
  const u4 = await ctx.signIn('member-4');

  // Create two apps A and B + route pricing for catalog check
  const appA = (await ctx.req(operator, 'POST', '/api/admin/apps', {
    name: 'AppA', redirectUris: ['https://a.example.test/cb'],
  })).json().app.id;
  const appB = (await ctx.req(operator, 'POST', '/api/admin/apps', {
    name: 'AppB', redirectUris: ['https://b.example.test/cb'],
  })).json().app.id;

  await seedMembershipProduct(ctx.req, operator, appA, {
    accountKind: 'enterprise', tierId: 'ent_a', priceMinor: 14900, baseCredits: 3000, bonusCredits: 500,
  });
  await seedMembershipProduct(ctx.req, operator, appB, {
    accountKind: 'enterprise', tierId: 'ent_b', priceMinor: 14900, baseCredits: 3000, bonusCredits: 500,
  });
  for (const appId of [appA, appB]) {
    const pubPrice = await ctx.req(operator, 'POST', `/api/admin/apps/${appId}/route-pricing/publish`, {
      accountKind: 'enterprise',
      creditsPerCurrency: 100,
      routes: [{
        routeId: 'ali:text-embedding-v4', modelId: 'text-embedding-v4', operation: 'embeddings',
        unit: '1k_tokens', baseAmountDecimal: '0.0007', unihubMultiplier: '1.25',
        minBillableQty: '0.04', meterVersion: 'v1', routerRateVersion: 'r1',
      }],
    });
    assert.equal(pubPrice.statusCode, 201, pubPrice.body);
  }

  // Owner creates enterprise account
  const entRes = await ctx.req(owner, 'POST', '/api/accounts/enterprises', { name: 'Acme Corp' });
  assert.equal(entRes.statusCode, 201, entRes.body);
  const entId = entRes.json().accountId;
  await ctx.req(owner, 'POST', '/api/session/account', { accountId: entId });

  // 2b-4: One-time invite links: u1 claims valid invite, replay fails (409), wrong-region & expired fail
  const futureExpiry = new Date(Date.now() + 86_400_000).toISOString();
  const inv1 = await ctx.req(owner, 'POST', `/api/accounts/${entId}/invites`, {
    role: 'member', expiresAt: futureExpiry,
  });
  assert.equal(inv1.statusCode, 201);
  const invToken = inv1.json().token;

  const claimOk = await ctx.req(u1, 'POST', `/api/invites/${invToken}/claim`, {});
  assert.equal(claimOk.statusCode, 201);
  assert.equal(claimOk.json().claimed, true);

  const claimReplay = await ctx.req(u2, 'POST', `/api/invites/${invToken}/claim`, {});
  assert.equal(claimReplay.statusCode, 409);

  // Direct DB expired & wrong-region invite checks
  const invExp = (await ctx.req(owner, 'POST', `/api/accounts/${entId}/invites`, {
    role: 'member', expiresAt: futureExpiry,
  })).json().token;
  const invExpHash = createHash('sha256').update(invExp).digest();
  await ctx.pool.query(
    "UPDATE enterprise_invites SET expires_at = statement_timestamp() - interval '1 hour' WHERE token_hash = $1",
    [invExpHash],
  );
  const claimExp = await ctx.req(u2, 'POST', `/api/invites/${invExp}/claim`, {});
  assert.equal(claimExp.statusCode, 409);

  const invCross = (await ctx.req(owner, 'POST', `/api/accounts/${entId}/invites`, {
    role: 'member', expiresAt: futureExpiry,
  })).json().token;
  const invCrossHash = createHash('sha256').update(invCross).digest();
  await ctx.pool.query('UPDATE enterprise_invites SET region = $1 WHERE token_hash = $2', ['us', invCrossHash]);
  const claimCross = await ctx.req(u2, 'POST', `/api/invites/${invCross}/claim`, {});
  assert.equal(claimCross.statusCode, 400);

  // Add u2, u3, u4 via invites so we have 5 members including owner
  for (const member of [u2, u3, u4]) {
    const tkn = (await ctx.req(owner, 'POST', `/api/accounts/${entId}/invites`, {
      role: 'member', expiresAt: futureExpiry,
    })).json().token;
    assert.equal((await ctx.req(member, 'POST', `/api/invites/${tkn}/claim`, {})).statusCode, 201);
  }
  for (const member of [u1, u2, u3, u4]) {
    await ctx.req(member, 'POST', '/api/session/account', { accountId: entId });
  }

  // Recharge enterprise wallet and buy 2 seats for App A + 2 seats for App B
  const payEnt = await ctx.req(owner, 'POST', '/api/wallet/recharges', { amountMinor: 100000 });
  await ctx.req(owner, 'POST', `/api/wallet/payment-orders/${payEnt.json().paymentOrder.id}/sandbox-settle`, {
    providerTradeNo: 'ent-sb-01',
  });
  await ctx.req(owner, 'POST', '/api/billing/checkout', { appId: appA, tierId: 'ent_a', quantity: 2 });
  await ctx.req(owner, 'POST', '/api/billing/checkout', { appId: appB, tierId: 'ent_b', quantity: 2 });
  await ctx.req(operator, 'POST', '/api/internal/billing/outbox/process', {});

  const subs = (await ctx.req(owner, 'GET', '/api/billing/subscriptions')).json().subscriptions;
  const subA = subs.find((s) => s.appId === appA);
  const subB = subs.find((s) => s.appId === appB);

  // 2b-3: 5 concurrent seat assignment requests on 2-seat App A -> exactly 2 succeed, 3 hit 409 seat_limit_reached
  const candidates = [owner, u1, u2, u3, u4];
  const results = await Promise.all(candidates.map((candidate) => ctx.req(
    owner, 'POST', `/api/accounts/${entId}/apps/${appA}/seats`, {
      subscriptionId: subA.id, userId: candidate.user.id,
    },
  )));
  const successes = results.filter((r) => r.statusCode === 201);
  const conflicts = results.filter((r) => r.statusCode === 409 && r.json().error === 'seat_limit_reached');
  assert.equal(successes.length, 2);
  assert.equal(conflicts.length, 3);

  // Determine first seated user on App A, also grant u1 seat on App A (revoke one if u1 wasn't winner) and u1 seat on App B
  const seatsA = (await ctx.req(owner, 'GET', `/api/accounts/${entId}/apps/${appA}/seats`)).json().seats;
  if (!seatsA.some((s) => s.userId === u1.user.id)) {
    await ctx.req(owner, 'DELETE', `/api/accounts/${entId}/apps/${appA}/seats/${seatsA[0].userId}?subscriptionId=${subA.id}`, {});
    const grantU1 = await ctx.req(owner, 'POST', `/api/accounts/${entId}/apps/${appA}/seats`, {
      subscriptionId: subA.id, userId: u1.user.id,
    });
    assert.equal(grantU1.statusCode, 201);
  }
  await ctx.req(owner, 'POST', `/api/accounts/${entId}/apps/${appB}/seats`, {
    subscriptionId: subB.id, userId: u1.user.id,
  });

  // Set member cycle limit for u1 on App A (2 credits = 2,000,000 units), start an in-flight hold (1 credit)
  await ctx.req(owner, 'PUT', `/api/accounts/${entId}/apps/${appA}/members/${u1.user.id}/limit`, {
    cycleLimitUnits: '2000000',
  });
  const holdRes = await ctx.req(u1, 'POST', '/api/billing/credits/holds', {
    appId: appA, requestId: 'u1-inflight-before-revoke', authorizedUnits: '1000000', pricingSnapshot: { model: 'm1' },
  });
  assert.equal(holdRes.statusCode, 201);
  const holdId = holdRes.json().hold.id;

  // Revoke u1's App A seat -> App A catalog blocked immediately, App B catalog still works!
  const revokeA = await ctx.req(owner, 'DELETE', `/api/accounts/${entId}/apps/${appA}/seats/${u1.user.id}?subscriptionId=${subA.id}`, {});
  assert.equal(revokeA.statusCode, 204);
  const catA = (await ctx.req(u1, 'GET', `/api/catalog/models?appId=${appA}`)).json();
  const catB = (await ctx.req(u1, 'GET', `/api/catalog/models?appId=${appB}`)).json();
  assert.equal(catA.hasAppSeat, false);
  assert.equal(catA.models.length, 0);
  assert.equal(catB.hasAppSeat, true);
  assert.equal(catB.models.length, 1);

  // New hold on App A for u1 is rejected (app_seat_required)
  const newHoldA = await ctx.req(u1, 'POST', '/api/billing/credits/holds', {
    appId: appA, requestId: 'u1-new-after-revoke', authorizedUnits: '500000', pricingSnapshot: { model: 'm1' },
  });
  assert.equal(newHoldA.statusCode, 403);

  // Removing u1 from enterprise cascades removal of App B seat too, yet original App A in-flight hold settles cleanly
  const removeMember = await ctx.req(owner, 'DELETE', `/api/accounts/${entId}/members/${u1.user.id}`, {});
  assert.equal(removeMember.statusCode, 204);
  const seatsBAfterRemove = (await ctx.req(owner, 'GET', `/api/accounts/${entId}/apps/${appB}/seats`)).json().seats;
  assert.equal(seatsBAfterRemove.length, 0);

  const settleOldHold = await ctx.req(operator, 'POST', `/api/billing/credits/holds/${holdId}/settle`, {
    settledUnits: '600000',
  });
  assert.equal(settleOldHold.statusCode, 200);
  assert.equal(settleOldHold.json().hold.settledUnits, '600000');

  const { rows: [usageRow] } = await ctx.pool.query(
    'SELECT held_units, settled_units FROM enterprise_member_cycle_usage WHERE account_id = $1 AND app_id = $2 AND user_id = $3',
    [entId, appA, u1.user.id],
  );
  assert.equal(usageRow.held_units, '0');
  assert.equal(usageRow.settled_units, '600000');
});
