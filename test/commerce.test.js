import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync, randomBytes, randomUUID, sign } from 'node:crypto';
import { createServer } from 'node:http';
import { test } from 'node:test';
import pg from 'pg';
import { buildApp } from '../src/app.js';
import { computeMinLedgerUnits } from '../src/commerce/validation.js';

async function testDatabase(t) {
  const connectionString = process.env.UNIHUB_TEST_DATABASE_URL;
  assert.ok(connectionString, 'set UNIHUB_TEST_DATABASE_URL to a dedicated PostgreSQL test database');
  const control = new pg.Pool({ connectionString });
  const schema = `unihub_2a_${randomBytes(8).toString('hex')}`;
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
  const clientId = 'unihub-2a-test';
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

  function change(session, method, url, payload) {
    return app.inject({ method, url, cookies: session.cookies, payload, headers: { 'x-csrf-token': session.csrf } });
  }

  return { app, pool, signIn, change };
}

test('Stage 2a: 应用列表按角色隔离、轮换 clientSecret 立即生效及我授权过的应用管理', async (t) => {
  const { app, signIn, change } = await fixture(t);
  const operator = await signIn('operator');
  const swOp = await signIn('sw-operator');
  const endUser = await signIn('end-user');

  const swRes = await change(operator, 'POST', '/api/admin/apps', {
    name: 'SuperWagie', description: 'AI Agent 工作台', redirectUris: ['https://sw.example.test/callback'],
  });
  const dfRes = await change(operator, 'POST', '/api/admin/apps', {
    name: 'DocFlow AI', description: '文档解析与检索', redirectUris: ['https://df.example.test/callback'],
  });
  assert.equal(swRes.statusCode, 201);
  assert.equal(dfRes.statusCode, 201);
  const sw = swRes.json();
  const df = dfRes.json();

  // Assign swOp only to SuperWagie
  assert.equal((await change(operator, 'PUT', `/api/admin/apps/${sw.app.id}/operators/${swOp.user.id}`)).statusCode, 204);

  // Platform operator sees both; app operator sees only SuperWagie; normal user gets 403
  assert.equal((await app.inject({ url: '/api/admin/apps', cookies: operator.cookies })).json().apps.length, 2);
  const swOpApps = (await app.inject({ url: '/api/admin/apps', cookies: swOp.cookies })).json().apps;
  assert.deepEqual(swOpApps.map((a) => a.id), [sw.app.id]);
  assert.equal((await app.inject({ url: '/api/admin/apps', cookies: endUser.cookies })).statusCode, 403);

  // Rotate clientSecret: old secret fails 401, new secret succeeds
  const loginRes = await app.inject({
    url: `/login?${new URLSearchParams({ app_id: sw.app.id, redirect_uri: 'https://sw.example.test/callback', state: 'st1' })}`,
    cookies: endUser.cookies,
  });
  assert.equal(loginRes.statusCode, 303);
  const code1 = new URL(loginRes.headers.location).searchParams.get('code');

  const rotRes = await change(operator, 'POST', `/api/admin/apps/${sw.app.id}/rotate-secret`);
  assert.equal(rotRes.statusCode, 200);
  const newSecret = rotRes.json().clientSecret;
  assert.notEqual(newSecret, sw.clientSecret);

  const exchangeWith = (secret, code) => app.inject({
    method: 'POST', url: '/api/apps/login/exchange',
    headers: { authorization: `Basic ${Buffer.from(`${sw.app.id}:${secret}`).toString('base64')}` },
    payload: { code, redirectUri: 'https://sw.example.test/callback' },
  });
  assert.equal((await exchangeWith(sw.clientSecret, code1)).statusCode, 401);
  assert.equal((await exchangeWith(newSecret, code1)).statusCode, 200);

  // User sees SuperWagie in authorized apps and can revoke it
  const authBefore = (await app.inject({ url: '/api/me/authorized-apps', cookies: endUser.cookies })).json();
  assert.deepEqual(authBefore.authorizedApps.map((a) => a.appId), [sw.app.id]);
  assert.equal((await change(endUser, 'DELETE', `/api/me/authorized-apps/${sw.app.id}`)).statusCode, 204);
  const authAfter = (await app.inject({ url: '/api/me/authorized-apps', cookies: endUser.cookies })).json();
  assert.equal(authAfter.authorizedApps.length, 0);
  assert.equal(df.app.description, '文档解析与检索');
});

test('Stage 2a (2a-1 & 2a-2): 双应用单卡独立上架/下架隔离草稿、按时间解析、ID回读与数据库不可变保护', async (t) => {
  const { app, pool, signIn, change } = await fixture(t);
  const operator = await signIn('operator');
  const swOp = await signIn('sw-operator');
  const buyer = await signIn('buyer');

  const swId = (await change(operator, 'POST', '/api/admin/apps', {
    name: 'SuperWagie', redirectUris: ['https://sw.example.test/cb'],
  })).json().app.id;
  const dfId = (await change(operator, 'POST', '/api/admin/apps', {
    name: 'DocFlow AI', redirectUris: ['https://df.example.test/cb'],
  })).json().app.id;
  await change(operator, 'PUT', `/api/admin/apps/${swId}/operators/${swOp.user.id}`);

  // Single-app operator cannot create drafts on DocFlow AI
  const forbiddenDraft = await change(swOp, 'POST', `/api/admin/apps/${dfId}/product-drafts`, {
    accountKind: 'personal', productType: 'membership', tierId: 'df_std', tierRank: 10,
    rule: { billingCycle: 'monthly', priceMinor: 5900, baseCredits: 1200, bonusCredits: 200 },
    display: { name: 'DocFlow 标准版', description: '文档解析', benefits: ['每月 1,200 积分'] },
  });
  assert.equal(forbiddenDraft.statusCode, 403);

  // Create 2 personal drafts + 1 enterprise draft on SuperWagie
  const proRes = await change(swOp, 'POST', `/api/admin/apps/${swId}/product-drafts`, {
    accountKind: 'personal', productType: 'membership', tierId: 'tier_pro_m', tierRank: 20,
    rule: { billingCycle: 'monthly', priceMinor: 9900, baseCredits: 2000, bonusCredits: 500, entitlementKeys: ['feature:ocr=true'] },
    display: { name: 'Pro 月付版', description: '高频个人创作', benefits: ['每月 2,000 基础积分'] },
  });
  const maxRes = await change(swOp, 'POST', `/api/admin/apps/${swId}/product-drafts`, {
    accountKind: 'personal', productType: 'membership', tierId: 'tier_max_m', tierRank: 40,
    rule: { billingCycle: 'monthly', priceMinor: 19900, baseCredits: 5000, bonusCredits: 1000 },
    display: { name: 'Max 月付版（草稿）', description: '尚未定稿', benefits: ['每月 5,000 基础积分'] },
  });
  const entRes = await change(swOp, 'POST', `/api/admin/apps/${swId}/product-drafts`, {
    accountKind: 'enterprise', productType: 'membership', tierId: 'tier_ent_m', tierRank: 100,
    rule: { billingCycle: 'monthly', priceMinor: 14900, baseCredits: 3000, bonusCredits: 500 },
    display: { name: 'Enterprise 席位', description: '企业共享池', benefits: ['每席 3,000 基础积分'] },
  });
  assert.equal(proRes.statusCode, 201);
  assert.equal(maxRes.statusCode, 201);
  assert.equal(entRes.statusCode, 201);
  const proId = proRes.json().card.id;
  const maxId = maxRes.json().card.id;
  const entId = entRes.json().card.id;

  // Publish ONLY Pro at t1: Max draft must NOT leak into live version!
  const t1 = '2026-11-28T00:00:00.000Z';
  const pub1 = await change(swOp, 'POST', `/api/admin/apps/${swId}/product-drafts/${proId}/publish`, { effectiveAt: t1 });
  assert.equal(pub1.statusCode, 201);
  const v1 = pub1.json().version;
  assert.deepEqual(v1.cardsSnapshot.map((c) => c.tierId), ['tier_pro_m']);

  // Duplicate effective_at in same scope returns 409
  const dupEff = await change(swOp, 'POST', `/api/admin/apps/${swId}/product-drafts/${maxId}/publish`, { effectiveAt: t1 });
  assert.equal(dupEff.statusCode, 409);

  // Edit published Pro -> status becomes 'modified', live storefront still shows old ¥99 (9900)
  const modPro = await change(swOp, 'PUT', `/api/admin/apps/${swId}/product-drafts/${proId}`, {
    accountKind: 'personal', productType: 'membership', tierId: 'tier_pro_m', tierRank: 20,
    rule: { billingCycle: 'monthly', priceMinor: 10900, baseCredits: 2200, bonusCredits: 500 },
    display: { name: 'Pro 月付版（新价）', description: '提价草稿', benefits: ['每月 2,200 基础积分'] },
  });
  assert.equal(modPro.json().card.publishStatus, 'modified');

  const sfAtT1 = (await app.inject({
    url: `/api/storefront/products?appId=${swId}&at=${encodeURIComponent('2026-11-28T00:30:00.000Z')}`,
    cookies: buyer.cookies,
  })).json();
  assert.equal(sfAtT1.storefronts[0].cards[0].rule.priceMinor, 9900);

  // Publish updated Pro at t2 (still without leaking Max draft!), then publish Enterprise card
  const t2 = '2026-11-28T02:00:00.000Z';
  const pub2 = await change(swOp, 'POST', `/api/admin/apps/${swId}/product-drafts/${proId}/publish`, { effectiveAt: t2 });
  assert.equal(pub2.statusCode, 201);
  assert.deepEqual(pub2.json().version.cardsSnapshot.map((c) => c.tierId), ['tier_pro_m']);
  assert.equal((await change(swOp, 'POST', `/api/admin/apps/${swId}/product-drafts/${entId}/publish`, { effectiveAt: t1 })).statusCode, 201);

  // Resolve at t1+30m -> v1 (9900); resolve at t2+30m -> v2 (10900); readback v1 by ID works
  const sfBeforeT2 = (await app.inject({
    url: `/api/storefront/products?appId=${swId}&at=${encodeURIComponent('2026-11-28T01:00:00.000Z')}`,
    cookies: buyer.cookies,
  })).json();
  const sfAfterT2 = (await app.inject({
    url: `/api/storefront/products?appId=${swId}&at=${encodeURIComponent('2026-11-28T03:00:00.000Z')}`,
    cookies: buyer.cookies,
  })).json();
  assert.equal(sfBeforeT2.storefronts[0].versionId, v1.id);
  assert.equal(sfAfterT2.storefronts[0].cards[0].rule.priceMinor, 10900);

  const readback = (await app.inject({ url: `/api/commerce/versions/${v1.id}`, cookies: swOp.cookies })).json();
  assert.equal(readback.version.cardsSnapshot[0].rule.priceMinor, 9900);

  // Unpublish Pro at t3 -> storefront has 0 cards at t3+, draft status becomes 'offline'
  const t3 = '2026-11-28T04:00:00.000Z';
  const unpub = await change(swOp, 'POST', `/api/admin/apps/${swId}/product-drafts/${proId}/unpublish`, { effectiveAt: t3 });
  assert.equal(unpub.statusCode, 201);
  assert.equal(unpub.json().card.publishStatus, 'offline');
  assert.equal(unpub.json().version.cardsSnapshot.length, 0);

  // Database immutability trigger forbids UPDATE / DELETE on published versions
  await assert.rejects(
    pool.query('UPDATE commerce_versions SET change_summary = $1 WHERE id = $2', ['tampered', v1.id]),
    /immutable/i,
  );
  await assert.rejects(
    pool.query('DELETE FROM commerce_versions WHERE id = $1', [v1.id]),
    /immutable/i,
  );
});

test('Stage 2a (2a-3): 路由定价最小粒度校验、同源目录多路由区间价、权益过滤与企业按应用席位隔离', async (t) => {
  const { app, pool, signIn, change } = await fixture(t);
  const operator = await signIn('operator');
  const owner = await signIn('ent-owner');
  const member = await signIn('ent-member');

  // Exact BigInt precision check: 0.0005 * 1.0 * 200 * 0.04 = 0.0040 credits = 4,000 units
  assert.equal(
    computeMinLedgerUnits({ baseAmountDecimal: '0.0005', unihubMultiplier: '1.00', creditsPerCurrency: 200, minBillableQty: '0.04' }),
    4000n,
  );

  const swId = (await change(operator, 'POST', '/api/admin/apps', {
    name: 'SuperWagie', redirectUris: ['https://sw.example.test/cb'],
  })).json().app.id;
  const dfId = (await change(operator, 'POST', '/api/admin/apps', {
    name: 'DocFlow AI', redirectUris: ['https://df.example.test/cb'],
  })).json().app.id;

  // Reject multiplier = 0 (granularity underflow)
  const badRate = await change(operator, 'POST', `/api/admin/apps/${swId}/route-pricing/publish`, {
    accountKind: 'enterprise',
    creditsPerCurrency: 200,
    routes: [{
      modelId: 'text-embedding-v4', routeId: 'route_zero', operation: 'embedding', unit: '1K tok',
      minBillableQty: '0.04', meterVersion: 'm_1', routerRateVersion: 'r_1',
      baseAmountDecimal: '0.0005', unihubMultiplier: '0',
    }],
  });
  assert.equal(badRate.statusCode, 400);
  assert.equal(badRate.json().error, 'granularity_underflow');

  // Publish valid multi-route pricing on SuperWagie (enterprise) and DocFlow AI (enterprise)
  const pubSw = await change(operator, 'POST', `/api/admin/apps/${swId}/route-pricing/publish`, {
    accountKind: 'enterprise',
    creditsPerCurrency: 200,
    routes: [
      {
        modelId: 'text-embedding-v4', routeId: 'route_ali_emb_a', operation: 'embedding', unit: '1K tok',
        minBillableQty: '0.04', meterVersion: 'm_ali_emb_02', routerRateVersion: 'r_ali_01',
        baseAmountDecimal: '0.0005', unihubMultiplier: '1.00',
      },
      {
        modelId: 'text-embedding-v4', routeId: 'route_ali_emb_b', operation: 'embedding', unit: '1K tok',
        minBillableQty: '0.04', meterVersion: 'm_ali_emb_02', routerRateVersion: 'r_ali_02',
        baseAmountDecimal: '0.0006', unihubMultiplier: '1.00',
      },
      {
        modelId: 'paddleocr-vl', routeId: 'route_baidu_ocr_vl', operation: 'ocr', unit: '页',
        minBillableQty: '1', meterVersion: 'm_ocr_page_01', routerRateVersion: 'r_ocr_01',
        baseAmountDecimal: '0.03', unihubMultiplier: '1.00', entitlementFilter: 'feature:ocr=true',
      },
    ],
  });
  assert.equal(pubSw.statusCode, 201);
  assert.equal((await change(operator, 'POST', `/api/admin/apps/${dfId}/route-pricing/publish`, {
    accountKind: 'enterprise',
    creditsPerCurrency: 200,
    routes: [{
      modelId: 'paddleocr-vl', routeId: 'route_baidu_ocr_vl', operation: 'ocr', unit: '页',
      minBillableQty: '1', meterVersion: 'm_ocr_page_01', routerRateVersion: 'r_ocr_01',
      baseAmountDecimal: '0.03', unihubMultiplier: '0.90',
    }],
  })).statusCode, 201);

  // Create enterprise, add member, switch member to enterprise
  const entId = (await change(owner, 'POST', '/api/accounts/enterprises', { name: '星云智算' })).json().accountId;
  await change(owner, 'PUT', `/api/accounts/${entId}/members/${member.user.id}`, {
    role: 'member', canRecharge: false, canPurchase: true, hasSeat: false,
  });
  await change(member, 'POST', '/api/session/account', { accountId: entId });

  // Grant seat ONLY on SuperWagie (not DocFlow AI) via Stage 2a per-app seat contract
  await pool.query(`
    INSERT INTO enterprise_app_seat_contracts (account_id, app_id, subscription_id, user_id)
    VALUES ($1, $2, $3, $4)`, [entId, swId, randomUUID(), member.user.id]);

  // SuperWagie catalog succeeds with price range "0.10 ~ 0.12 积分 / 1K tok" and respects entitlementFilter
  const swCatNoOcr = (await app.inject({
    url: `/api/catalog/models?appId=${swId}&entitlements=`,
    cookies: member.cookies,
  })).json();
  assert.equal(swCatNoOcr.hasAppSeat, true);
  assert.deepEqual(swCatNoOcr.models.map((m) => m.modelId), ['text-embedding-v4']);
  assert.equal(swCatNoOcr.models[0].pricing[0].display, '0.10 ~ 0.12 积分 / 1K tok');

  const swCatWithOcr = (await app.inject({
    url: `/api/catalog/models?appId=${swId}&entitlements=feature:ocr=true`,
    cookies: member.cookies,
  })).json();
  assert.deepEqual(swCatWithOcr.models.map((m) => m.modelId).sort(), ['paddleocr-vl', 'text-embedding-v4']);

  // DocFlow AI has no seat -> returns hasAppSeat=false and empty models
  const dfCat = (await app.inject({
    url: `/api/catalog/models?appId=${dfId}`,
    cookies: member.cookies,
  })).json();
  assert.equal(dfCat.hasAppSeat, false);
  assert.equal(dfCat.models.length, 0);

  // Immutability check on route_pricing_versions
  await assert.rejects(
    pool.query('DELETE FROM route_pricing_versions WHERE id = $1', [pubSw.json().version.id]),
    /immutable/i,
  );
});
