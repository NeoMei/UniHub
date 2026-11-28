import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes, randomUUID } from 'node:crypto';
import { mkdtemp, rm } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import pg from 'pg';
import { buildApp } from '../../src/app.js';
import { createSession, readSession } from '../../src/identity.js';
import { applyDeclarativeSeed } from '../../src/seed/apply-seed.js';

export const model = 'deepseek-v4-pro';
export const routeId = `deepseek:${model}`;
export const meterVersion = 'deepseek-v4-pro-chat-v1';
export const routerRateVersion = 'deepseek-v4-pro-cn-20261017-v1';
export const grantCredits = 5;
export const secret = () => randomBytes(32).toString('hex');
const host = '127.0.0.1';

async function serve(resources, handler) {
  const server = createServer(handler);
  await new Promise((ok, fail) => server.once('error', fail).listen(0, host, ok));
  resources.defer(() => new Promise((ok, fail) => {
    server.close(err => err ? fail(err) : ok());
    server.closeAllConnections();
  }));
  return `http://${host}:${server.address().port}`;
}

// A child gets only its fixture settings. In particular no ambient DB URLs, proxies,
// provider credentials, .env files, or log directories are used by the bridge.
function childProcess(resources, executable, args, cwd, env = {}) {
  const child = spawn(executable, args, {
    cwd, stdio: 'ignore', env: { PATH: process.env.PATH, LC_ALL: 'C', ...env },
  });
  const done = new Promise(resolveExit => {
    child.once('error', () => resolveExit(-1));
    child.once('exit', code => resolveExit(code ?? -1));
  });
  resources.defer(async () => {
    if (child.pid && child.exitCode === null && child.signalCode === null) {
      child.kill('SIGTERM');
      const exited = await Promise.race([done.then(() => true), delay(3000, false, { ref: false })]);
      if (!exited) child.kill('SIGKILL');
    }
    await done;
  });
  return done;
}

async function ready(label, done, probe, signal) {
  const deadline = Date.now() + 30_000;
  for (;;) {
    signal.throwIfAborted();
    const state = await Promise.race([done.then(() => 'exited'), probe().then(Boolean, () => false)]);
    assert.notEqual(state, 'exited', `${label} exited during startup (check executable and settings)`);
    if (state) return;
    assert.ok(Date.now() < deadline, `${label} startup timed out`);
    await delay(100, undefined, { signal });
  }
}

export async function startDatabase(resources, signal) {
  const dir = await mkdtemp(join(tmpdir(), 'wanliu-qa-'));
  resources.defer(() => rm(dir, { recursive: true, force: true }));
  const bin = name => process.env.PG_BINDIR ? resolve(process.env.PG_BINDIR, name) : name;
  const data = join(dir, 'pg');
  const dbUser = 'wanliu_qa';
  // Private socket directory (mkdtemp mode 0700), no TCP listener and no password file.
  const init = childProcess(resources, bin('initdb'), ['-D', data, '-U', dbUser, '--auth-local=trust', '--auth-host=reject', '--encoding=UTF8', '--no-locale'], dir);
  assert.equal(await init, 0, 'initdb failed (check PG_BINDIR and run as a non-root user)');
  signal.throwIfAborted();
  const done = childProcess(resources, bin('postgres'), ['-D', data, '-h', '', '-k', dir, '-p', '5432'], dir);
  const schema = `qa_${randomBytes(8).toString('hex')}`;
  const pool = new pg.Pool({ host: dir, port: 5432, database: 'postgres', user: dbUser,
    ssl: false, connectionTimeoutMillis: 1500, options: `-c search_path=${schema}` });
  resources.defer(() => pool.end());
  await ready('PostgreSQL', done, () => pool.query('SELECT 1'), signal);
  await pool.query(`CREATE SCHEMA ${schema}`);
  assert.equal((await pool.query('SELECT current_schema() AS name')).rows[0].name, schema);
  return { pool, dir };
}

export async function startUniHub(resources, pool, gatewayServiceToken) {
  let origin;
  origin = await serve(resources, (req, res) => {
    if (req.url !== '/.well-known/openid-configuration') return res.writeHead(404).end();
    res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify({
      issuer: origin, authorization_endpoint: `${origin}/auth`, token_endpoint: `${origin}/token`, jwks_uri: `${origin}/jwks`,
    }));
  });
  const operator = 'qa-fixture-operator';
  const app = await buildApp({ origin: new URL(origin), issuer: new URL(origin),
    clientId: 'wanliu-smoke', clientSecret: secret(), sessionKey: randomBytes(32), pool,
    region: 'cn', currency: 'CNY', operatorSubjects: new Set([operator]), gatewayServiceToken,
    paymentConfig: { allowSandboxSettle: true },
  });
  resources.defer(() => app.close());
  app.log.level = 'silent';
  const internalURL = await app.listen({ host, port: 0 });
  async function qaSession(subject) {
    const csrf = 'qa-fixture-csrf'; // The authentication cookie is encrypted in memory.
    const id = await createSession(pool, { issuer: new URL(origin).href, subject, name: subject,
      email: `${subject}@example.test`, csrf, lifetime: 600 });
    const session = await readSession(pool, id);
    const cookie = app.encodeSecureSession(app.createSecureSession({ sessionId: id }));
    async function post(url, payload, status = 200) {
      const res = await app.inject({ method: 'POST', url, payload, cookies: { unihub_session: cookie },
        headers: { 'x-csrf-token': csrf } });
      assert.equal(res.statusCode, status, `QA fixture ${url} returned HTTP ${res.statusCode}`);
      return res.json();
    }
    return { ...session, post };
  }
  const admin = await qaSession(operator);
  const payer = await qaSession(`qa-${randomUUID()}`);
  const { app: product } = await applyDeclarativeSeed(pool, {
    appName: 'Wanliu QA', appDescription: 'Disposable billing smoke', redirectUris: [`${origin}/callback`],
    operatorUserId: admin.userId, region: 'cn', currency: 'CNY', creditsPerCurrency: 200,
    personalCards: [{ tierId: 'qa_monthly', tierRank: 1, productType: 'membership',
      rule: { billingCycle: 'monthly', priceMinor: 100, baseCredits: grantCredits },
      display: { name: 'QA membership', description: 'Local QA points', benefits: [] } }],
    routes: [{ modelId: model, routeId, operation: 'chat', unit: '1K tokens', minBillableQty: '0.001',
      meterVersion, routerRateVersion, baseAmountDecimal: '0.027', unihubMultiplier: '1' }],
  });
  const { paymentOrder } = await payer.post('/api/wallet/recharges', { amountMinor: 100 }, 201);
  await payer.post(`/api/wallet/payment-orders/${paymentOrder.id}/sandbox-settle`, { providerTradeNo: randomUUID() });
  await payer.post('/api/billing/checkout', { appId: product.id, tierId: 'qa_monthly', quantity: 1 }, 201);
  const outbox = await admin.post('/api/internal/billing/outbox/process', {});
  assert.equal(outbox.failed.length, 0, 'QA entitlement outbox failed');
  const key = await payer.post('/api/keys', { appId: product.id, name: 'Disposable QA key' }, 201);
  assert.ok(key.plaintextKey?.startsWith('uh_live_'), 'QA key issuance failed');
  return { internalURL, apiKey: key.plaintextKey, accountId: payer.activeAccountId, userId: payer.userId, appId: product.id };
}

export async function startBridge(resources, { dir, internalURL, gatewayServiceToken, upstream, calendar, signal }) {
  // The Go executable accepts a port number, not a pre-bound socket.
  let url;
  { await using reservation = new AsyncDisposableStack(); url = await serve(reservation, () => {}); }
  const executable = process.env.WANLIU_GATEWAY_BIN ? resolve(process.env.WANLIU_GATEWAY_BIN) : 'new-api';
  const done = childProcess(resources, executable, ['--log-dir='], dir, {
    HOST: host, SERVER_BIND_ADDRESS: host, PORT: new URL(url).port, GIN_MODE: 'release', SQLITE_PATH: 'file:qa?mode=memory&cache=shared',
    SESSION_SECRET: secret(), UNIHUB_V2_BILLING_ENABLED: 'true', UNIHUB_INTERNAL_URL: internalURL,
    UNIHUB_GATEWAY_SERVICE_TOKEN: gatewayServiceToken, UNIHUB_EXECUTOR_ID: 'wanliu-smoke',
    UNIHUB_REGION: 'cn', UNIHUB_CURRENCY: 'CNY', UNIHUB_UPSTREAM_SIMULATOR: 'false',
    UNIHUB_DEEPSEEK_CN_CALENDAR_JSON: calendar,
    UNIHUB_UPSTREAM_BASE_URL: upstream.url, UNIHUB_UPSTREAM_API_KEY: upstream.key,
  });
  // Authenticate this particular bridge before sending it the disposable payer key.
  await ready('New API', done, async () => {
    const res = await fetch(`${url}/api/internal/unihub/keys/sync`, { method: 'POST', redirect: 'error',
      headers: { 'x-unihub-service-token': gatewayServiceToken, 'content-type': 'application/json' },
      body: '{"action":"smoke"}', signal: AbortSignal.timeout(1000) });
    return res.ok && (await res.json()).action === 'smoke';
  }, signal);
  return url;
}

export async function mockNative(resources) {
  const key = secret();
  const id = `chatcmpl-${randomUUID()}`;
  let completions = 0;
  const url = await serve(resources, async (req, res) => {
    res.setHeader('content-type', 'application/json');
    res.setHeader('X-Oneapi-Request-Id', randomUUID());
    if (req.headers.authorization !== `Bearer ${key}`) return res.writeHead(401).end('{}');
    if (req.method === 'GET' && req.url === '/v1/models') return res.end(JSON.stringify({ data: [{ id: model }] }));
    if (req.method !== 'POST' || req.url !== '/v1/chat/completions') return res.writeHead(404).end('{}');
    completions++;
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = JSON.parse(Buffer.concat(chunks));
    if (completions !== 1 || body.model !== model || body.stream !== false || body.max_tokens !== 8) return res.writeHead(400).end('{}');
    res.end(JSON.stringify({ id, object: 'chat.completion', model, choices: [{ message: { role: 'assistant', content: 'OK' }, finish_reason: 'stop' }],
      usage: { prompt_tokens: 11, prompt_cache_hit_tokens: 3, prompt_cache_miss_tokens: 8, completion_tokens: 1, total_tokens: 12 } }));
  });
  return { url, key, verify(completion) { assert.equal(completions, 1); assert.equal(completion.id, id); } };
}

// Observe the native response on its own HTTP hop. The bridge may generate a
// different X-Oneapi-Request-Id for the outer request. Forward no caller cookies
// or routing headers. A duplicate billable request never reaches the target.
export async function observeNative(resources, target, signal) {
  const evidence = { catalogs: 0, posts: 0 };
  const url = await serve(resources, async (req, res) => {
    try {
      const catalog = req.method === 'GET' && req.url === '/v1/models';
      const chat = req.method === 'POST' && req.url === '/v1/chat/completions';
      if (req.headers.authorization !== `Bearer ${target.key}`) return res.writeHead(401).end();
      if (!catalog && !chat) return res.writeHead(404).end();
      let body;
      if (chat) {
        if (++evidence.posts !== 1) return res.writeHead(409).end();
        const chunks = [];
        for await (const chunk of req) chunks.push(chunk);
        body = Buffer.concat(chunks);
        const payload = JSON.parse(body);
        if (payload.model !== model || payload.max_tokens !== 8 || payload.stream === true ||
            payload.messages?.length !== 1 || payload.messages[0].content !== 'Reply OK.') return res.writeHead(400).end();
        evidence.start = new Date();
      }
      const native = await fetch(target.url + req.url, { method: req.method, headers: {
        authorization: req.headers.authorization, 'content-type': 'application/json',
        ...(req.headers['x-request-id'] ? { 'x-request-id': req.headers['x-request-id'] } : {}),
      }, body, redirect: 'error', signal: AbortSignal.any([signal, AbortSignal.timeout(175_000)]) });
      if (chat) evidence.status = native.status;
      const raw = await native.text();
      if (native.ok) {
        if (catalog) {
          const listing = JSON.parse(raw);
          if (listing.data?.some(entry => entry.id === model)) evidence.catalogs++;
        } else {
          const completion = JSON.parse(raw);
          evidence.end = new Date();
          evidence.id = completion.id;
          evidence.usage = completion.usage;
          evidence.requestId = native.headers.get('x-oneapi-request-id');
        }
      }
      res.statusCode = native.status;
      res.setHeader('content-type', native.headers.get('content-type') ?? 'application/json');
      const requestId = native.headers.get('x-oneapi-request-id');
      if (requestId) res.setHeader('x-oneapi-request-id', requestId);
      res.end(raw);
    } catch {
      res.writeHead(502).end('{}');
    }
  });
  return { url, key: target.key, evidence };
}
