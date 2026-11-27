const MAX_SIGNED_BIGINT = 9_223_372_036_854_775_807n;
const CREDIT_SCALE = 1_000_000n;

const PROVIDERS_BY_REGION = new Map([
  ['cn:CNY', new Set(['wechat', 'alipay'])],
  ['us:USD', new Set(['stripe'])],
]);
const UUID_RE = /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i;
const ORDER_TYPES = new Set(['subscribe', 'renew', 'upgrade', 'seat_expand', 'credit_pack']);

export function providerAllowedForRegion(region, currency, provider) {
  return PROVIDERS_BY_REGION.get(`${region}:${currency}`)?.has(provider) ?? false;
}

export function parseAmountMinor(value) {
  let text;
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || value < 0) return null;
    text = String(value);
  } else if (typeof value === 'string') {
    text = value.trim();
    if (!/^(?:0|[1-9]\d*)$/.test(text)) return null;
  } else {
    return null;
  }

  try {
    const amount = BigInt(text);
    return amount <= MAX_SIGNED_BIGINT ? amount : null;
  } catch {
    return null;
  }
}

export function creditsToUnits(value) {
  const text = typeof value === 'number' ? String(value) : typeof value === 'string' ? value.trim() : '';
  if (!/^(?:0|[1-9]\d*)(?:\.\d{1,6})?$/.test(text)) return null;
  const [whole, fraction = ''] = text.split('.');
  const units = BigInt(whole) * CREDIT_SCALE + BigInt(fraction.padEnd(6, '0'));
  return units <= MAX_SIGNED_BIGINT ? units : null;
}

export function allocateCreditUnits(batches, requestedUnits) {
  if (!Array.isArray(batches) || typeof requestedUnits !== 'bigint' || requestedUnits < 0n) {
    throw new TypeError('invalid credit allocation');
  }
  if (requestedUnits === 0n) return [];

  const candidates = batches
    .map((batch) => ({
      ...batch,
      availableUnits: typeof batch.availableUnits === 'bigint' ? batch.availableUnits : BigInt(batch.availableUnits),
    }))
    .filter((batch) => batch.availableUnits > 0n)
    .sort((left, right) => {
      const expiry = new Date(left.expiresAt).getTime() - new Date(right.expiresAt).getTime();
      if (expiry) return expiry;
      const priority = (sourceType) => sourceType === 'plan_base' ? 0 : sourceType === 'credit_pack' ? 1 : 2;
      const diff = priority(left.sourceType) - priority(right.sourceType);
      return diff || String(left.id).localeCompare(String(right.id));
    });

  const totalAvailable = candidates.reduce((sum, batch) => sum + batch.availableUnits, 0n);
  if (totalAvailable < requestedUnits) throw new Error('insufficient credits');

  let remaining = requestedUnits;
  const allocation = [];
  for (const batch of candidates) {
    if (remaining === 0n) break;
    const units = batch.availableUnits < remaining ? batch.availableUnits : remaining;
    allocation.push({ batchId: batch.id, units });
    remaining -= units;
  }
  return allocation;
}

export function normalizeCheckoutInput(body) {
  if (!body || typeof body !== 'object') return null;
  const appId = typeof body.appId === 'string' ? body.appId.trim() : '';
  const versionId = body.versionId === undefined || body.versionId === null || body.versionId === ''
    ? null : typeof body.versionId === 'string' ? body.versionId.trim() : '';
  const tierId = typeof body.tierId === 'string' ? body.tierId.trim() : '';
  const quantity = body.quantity === undefined ? 1 : body.quantity;
  const orderType = body.orderType ?? 'subscribe';
  if (!UUID_RE.test(appId) || (versionId !== null && !UUID_RE.test(versionId))) return null;
  if (!/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/.test(tierId)) return null;
  if (!Number.isSafeInteger(quantity) || quantity < 1 || quantity > 100_000) return null;
  if (!ORDER_TYPES.has(orderType) || typeof body.autoRenew !== 'undefined' && typeof body.autoRenew !== 'boolean') return null;
  return { appId, versionId, tierId, quantity, orderType, autoRenew: body.autoRenew === true };
}

export const BILLING_UNITS_PER_CREDIT = CREDIT_SCALE;
