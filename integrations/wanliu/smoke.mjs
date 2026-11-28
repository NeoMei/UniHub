import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { parseDecimalScaled12 } from '../../src/commerce/validation.js';
import { creditBalance } from '../../src/billing/credits.js';
import { grantCredits, meterVersion, mockNative, model, observeNative, routeId, routerRateVersion, secret, startBridge, startDatabase, startUniHub } from './smoke-fixture.mjs';

const usage = `node integrations/wanliu/smoke.mjs --mock | --real
Requires Node 24.2+, installed UniHub dependencies, initdb/postgres on PATH or PG_BINDIR,
and a patched new-api on PATH or WANLIU_GATEWAY_BIN pointing to the executable.
--mock runs the actual bridge and UniHub against a loopback native fixture.
--real requires UNIHUB_UPSTREAM_BASE_URL (origin, no /v1), UNIHUB_UPSTREAM_API_KEY (QA bearer).
For a real run during a weekday peak window, set UNIHUB_DEEPSEEK_CN_CALENDAR_JSON to a versioned day map,
e.g. {"version":"qa-calendar","days":{"2026-11-30":"workday"}} (or holiday).
The harness sends one billable POST and does not retry.
Peak: Beijing Mon-Fri 09:00-12:00 and 14:00-18:00 excluding Chinese statutory holidays.
All DB, identity, payment and UniHub service settings are disposable and created by the harness.`;

function realNative() {
  const key = process.env.UNIHUB_UPSTREAM_API_KEY?.trim();
  assert.ok(key, 'missing UNIHUB_UPSTREAM_API_KEY');
  assert.ok(URL.canParse(process.env.UNIHUB_UPSTREAM_BASE_URL), 'set UNIHUB_UPSTREAM_BASE_URL to the native gateway origin');
  const url = new URL(process.env.UNIHUB_UPSTREAM_BASE_URL);
  const tunnel = url.protocol === 'http:' && ['127.0.0.1', '[::1]', 'localhost'].includes(url.hostname);
  assert.ok((url.protocol === 'https:' || tunnel) && !url.username && !url.password && !url.search && !url.hash && url.pathname === '/',
    'native URL must be an HTTPS origin or a loopback HTTP tunnel origin');
  return { url: url.origin, key };
}

// DeepSeek V4 Pro in CNY per million tokens. Rates are integer fen so that a
// sub-fen request remains exact through conversion to UniHub microcredits.
export function quote(usage, period) {
  assert.ok(['offpeak', 'peak'].includes(period), 'invalid tariff period');
  const count = n => Number.isSafeInteger(n) && n >= 0;
  const prompt = usage?.prompt_tokens;
  const output = usage?.completion_tokens;
  assert.ok(usage?.billing_usage?.estimated !== true, 'native token counts are estimated');
  const total = usage?.total_tokens ?? prompt + output;
  assert.ok(count(prompt) && prompt > 0 && count(output) && output > 0 && count(total) && prompt + output === total, 'invalid native token counts');
  const cached = usage.prompt_tokens_details?.cached_tokens;
  let hit = usage.prompt_cache_hit_tokens ?? cached;
  if (hit == null && count(usage.prompt_cache_miss_tokens)) hit = prompt - usage.prompt_cache_miss_tokens;
  const miss = usage.prompt_cache_miss_tokens ?? prompt - hit;
  assert.ok(count(hit) && count(miss) && hit + miss === prompt && (cached == null || cached === hit), 'missing or inconsistent native cache breakdown');
  const scale = period === 'offpeak' ? 1n : 2n;
  const hundredMillionthsCny = (BigInt(hit) * 15n + BigInt(miss) * 450n + BigInt(output) * 1350n) * scale;
  // 200 credits/CNY, 1,000,000 ledger units/credit, QA multiplier = 1.
  return { period, hit, miss, output, total, baseCny: `${hundredMillionthsCny / 100000000n}.${String(hundredMillionthsCny % 100000000n).padStart(8, '0')}`,
    expectedUnits: String(hundredMillionthsCny * 2n) };
}

export function tariffAt(instant, calendar = process.env.UNIHUB_DEEPSEEK_CN_CALENDAR_JSON) {
  const beijing = new Date(instant.getTime() + 8 * 3600_000);
  const hour = beijing.getUTCHours();
  if ([0, 6].includes(beijing.getUTCDay()) || hour < 9 || (hour >= 12 && hour < 14) || hour >= 18) return 'offpeak';
  const day = beijing.toISOString().slice(0, 10);
  const kind = calendar && JSON.parse(calendar).days?.[day];
  assert.ok(kind === 'workday' || kind === 'holiday', `set UNIHUB_DEEPSEEK_CN_CALENDAR_JSON for ${day} (workday/holiday)`);
  return kind === 'workday' ? 'peak' : 'offpeak';
}

async function snapshot(pool, qa) {
  const credits = await creditBalance(pool, qa);
  const { rows: [wallet] } = await pool.query(`SELECT available_minor, arrears_minor,
    (SELECT count(*) FROM cash_ledger_entries WHERE account_id = $1) AS entries
    FROM wallet_accounts WHERE account_id = $1`, [qa.accountId]);
  return { availableUnits: credits.availableUnits, heldUnits: credits.heldUnits, settledUnits: credits.settledUnits, wallet };
}

async function run(mode, signal) {
  const now = new Date();
  const beijingDay = new Date(now.getTime() + 8 * 3600_000).toISOString().slice(0, 10);
  const calendar = mode === '--mock'
    ? JSON.stringify({ version: 'qa-fixture', days: { [beijingDay]: 'workday' } })
    : process.env.UNIHUB_DEEPSEEK_CN_CALENDAR_JSON ?? '';
  tariffAt(now, calendar); // validate the calendar before dispatch
  await using resources = new AsyncDisposableStack();
  const native = mode === '--mock' ? await mockNative(resources) : realNative();
  const upstream = await observeNative(resources, native, signal);
  const { pool, dir } = await startDatabase(resources, signal);
  const gatewayServiceToken = secret();
  const qa = await startUniHub(resources, pool, gatewayServiceToken);
  const bridge = await startBridge(resources, { ...qa, dir, gatewayServiceToken, upstream, calendar, signal });
  const headers = { authorization: `Bearer ${qa.apiKey}`, 'content-type': 'application/json' };
  const requestId = `smoke-${randomUUID()}`;
  async function request(path, options) {
    const res = await fetch(bridge + path, { headers, redirect: 'error',
      signal: AbortSignal.any([signal, AbortSignal.timeout(190_000)]), ...options });
    if (res.status !== 200) {
      const { rows: [hold] } = await pool.query('SELECT id, status FROM credit_holds WHERE account_id = $1 AND request_id = $2', [qa.accountId, requestId]);
      assert.fail(`${path} returned HTTP ${res.status}; native completion HTTP ${upstream.evidence.status ?? 'unobserved'}; hold ${hold?.id ?? 'none'} ${hold?.status ?? ''}`);
    }
    return { headers: res.headers, body: await res.json() };
  }
  const before = await snapshot(pool, qa);
  assert.equal(before.availableUnits, String(BigInt(grantCredits) * 1000000n), 'QA membership grant missing');
  const catalog = (await request('/v1/models')).body;
  const listed = catalog.data?.find(entry => entry.id === model);
  assert.ok(catalog.app?.id === qa.appId && listed?.routes?.some(route => route.routeId === routeId && route.meterVersion === meterVersion && route.routerRateVersion === routerRateVersion), 'QA route missing from bridge catalog');
  assert.deepEqual(listed.pricing.map(({ minLedgerUnits, maxLedgerUnits }) => [minLedgerUnits, maxLedgerUnits]), [
    ['30000000', '60000000'], ['900000000', '1800000000'], ['2700000000', '5400000000'],
  ], 'catalog prices must match the published input, cache and output tariff');
  const completion = await request('/v1/chat/completions', { method: 'POST', headers: { ...headers, 'x-request-id': requestId },
    body: JSON.stringify({ model, messages: [{ role: 'user', content: 'Reply OK.' }], stream: false, max_tokens: 8, thinking: { type: 'disabled' } }) });
  const body = completion.body;
  assert.ok(typeof body.id === 'string' && body.id && body.model === model && body.choices?.[0]?.message?.content?.trim(), 'incomplete native chat completion');
  const observed = upstream.evidence;
  assert.ok(observed.catalogs > 0 && observed.posts === 1 && observed.end, 'native catalog/completion was not observed exactly as requested');
  assert.ok(body.id === observed.id && /^[a-zA-Z0-9._:-]{1,160}$/.test(observed.requestId ?? '') &&
    ![qa.apiKey, native.key, gatewayServiceToken].includes(observed.requestId), 'missing or invalid native request correlation');
  assert.ok(completion.headers.get('x-oneapi-request-id') === observed.requestId, 'bridge did not return the native X-Oneapi-Request-Id');
  native.verify?.(body);
  const period = tariffAt(observed.start, calendar);
  assert.equal(tariffAt(observed.end, calendar), period, 'native call crossed tariff boundary');
  const price = quote(observed.usage, period);
  assert.deepEqual(quote(body.usage, period), price, 'relayed usage differs from native response');
  const holdId = completion.headers.get('x-unihub-hold-id');
  const { rows } = await pool.query('SELECT * FROM credit_holds');
  assert.equal(rows.length, 1, 'expected exactly one QA hold');
  const [hold] = rows;
  assert.ok(hold.id === holdId && hold.request_id === requestId && hold.account_id === qa.accountId && hold.app_id === qa.appId && hold.user_id === qa.userId, 'hold does not belong to this request/payer');
  assert.ok(hold.status === 'settled' && hold.committed_by === 'wanliu-smoke' && hold.commit_attempt === 1 && hold.upstream_ref, 'hold was not committed and settled by the bridge');
  assert.ok(hold.pricing_snapshot.routeId === routeId && hold.pricing_snapshot.meterVersion === meterVersion &&
    hold.pricing_snapshot.routerRateVersion === routerRateVersion && completion.headers.get('x-unihub-route-id') === routeId, 'route mismatch');
  assert.ok(hold.actual_usage_json?.qty === price.total && hold.actual_usage_json.modelId === model &&
    hold.actual_usage_json.nativeRequestId === observed.requestId && hold.actual_usage_json.cacheHitTokens === price.hit &&
    hold.actual_usage_json.cacheMissTokens === price.miss && hold.actual_usage_json.completionTokens === price.output &&
    hold.actual_usage_json.promptTokens === price.hit + price.miss, 'persisted usage does not match native response');
  const settled = BigInt(hold.settled_units);
  assert.ok(settled > 0n && hold.settled_units === completion.headers.get('x-unihub-settled-units'), 'settled response header mismatch');
  assert.equal(BigInt(hold.authorized_units), settled + BigInt(hold.released_units), 'unaccounted hold units');
  const after = await snapshot(pool, qa);
  assert.deepEqual(after.wallet, before.wallet, 'QA cash ledger changed during inference');
  assert.equal(BigInt(before.availableUnits) - BigInt(after.availableUnits), settled, 'QA available points do not reconcile');
  assert.equal(BigInt(after.settledUnits) - BigInt(before.settledUnits), settled, 'QA settled points do not reconcile');
  assert.equal(after.heldUnits, '0', 'QA points still held');
  const storedCny = hold.base_amount_json?.amount;
  assert.ok(typeof storedCny === 'string' && parseDecimalScaled12(storedCny) !== null && hold.base_amount_json.currency === 'CNY', 'missing persisted CNY base amount');
  const official = hold.official_amount_json;
  assert.ok(official?.currency === 'CNY' && official.routerRateVersion === routerRateVersion, 'missing official rate version');
  const officialTier = period === 'peak' ? 'peak' : 'off_peak';
  const priceMatches = official.tier === officialTier && parseDecimalScaled12(official.amount) === parseDecimalScaled12(price.baseCny) &&
    parseDecimalScaled12(storedCny) === parseDecimalScaled12(price.baseCny) && hold.settled_units === price.expectedUnits;
  return { passed: priceMatches, mode: mode.slice(2), model, accountId: qa.accountId, requestId, holdId, holdStatus: hold.status,
    nativeRequestId: observed.requestId, recordedNativeRequestId: hold.actual_usage_json.nativeRequestId, routeId, meterVersion, routerRateVersion, nativeCompletionIdSha256: createHash('sha256').update(body.id).digest('hex'), nativeUsageAndPrice: price,
    billedCny: storedCny, billedTier: official.tier, authorizedUnits: hold.authorized_units, settledUnits: hold.settled_units, releasedUnits: hold.released_units, before, after };
}

async function main() {
  const mode = process.argv[2];
  if (process.argv.length !== 3 || !['--mock', '--real'].includes(mode)) {
    console.log(usage);
    process.exitCode = mode === '--help' ? 0 : 2;
    return;
  }
  const controller = new AbortController();
  const interrupt = () => controller.abort();
  process.once('SIGINT', interrupt);
  process.once('SIGTERM', interrupt);
  try {
    const report = await run(mode, controller.signal);
    console.log(JSON.stringify(report, null, 2));
    assert.ok(report.passed, 'official component price differs from the recorded settlement');
  } finally {
    process.off('SIGINT', interrupt);
    process.off('SIGTERM', interrupt);
  }
}

if (import.meta.main) main().catch(error => {
  // Never dump native responses, subprocess logs, HTTP headers, or error causes.
  console.error(error instanceof assert.AssertionError ? error.message : `Smoke failed (${error.code ?? error.name})`);
  process.exitCode = 1;
});
