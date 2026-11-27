const ACCOUNT_KINDS = new Set(['personal', 'enterprise']);
const PRODUCT_TYPES = new Set(['membership', 'credit_pack']);
const BILLING_CYCLES = new Set(['monthly', 'yearly']);
const CARRYOVER_POLICIES = new Set(['expire_each_cycle', 'carryover_next']);
const DOWNGRADE_POLICIES = new Set(['period_end', 'forbidden']);
const PACK_ELIGIBILITIES = new Set(['active_member', 'any_user', 'min_tier_pro']);

const DECIMAL_RE = /^(?:0|[1-9]\d*)(?:\.\d{1,12})?$/;
const TIER_ID_RE = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$/;
const ENTITLEMENT_KEY_RE = /^[a-zA-Z0-9_.:-]+=[a-zA-Z0-9_.:-]+$/;

export const LEDGER_UNITS_PER_CREDIT = 1_000_000n;
const SCALE_12 = 1_000_000_000_000n;

export function parseDecimalScaled12(value) {
  const str = typeof value === 'number' ? value.toFixed(12).replace(/\.?0+$/, '') : typeof value === 'string' ? value.trim() : '';
  if (!DECIMAL_RE.test(str)) return null;
  const [whole, frac = ''] = str.split('.');
  return BigInt(whole) * SCALE_12 + BigInt(frac.padEnd(12, '0'));
}

/**
 * Exact BigInt fixed-point calculation of ledger units (1 credit = 1,000,000 units)
 * for one minimum billable quantity on a route:
 *   round(baseAmount * unihubMultiplier * creditsPerCurrency * minBillableQty * 1_000_000)
 */
export function computeMinLedgerUnits({ baseAmountDecimal, unihubMultiplier, creditsPerCurrency, minBillableQty }) {
  const base = parseDecimalScaled12(baseAmountDecimal);
  const mult = parseDecimalScaled12(unihubMultiplier);
  const qty = parseDecimalScaled12(minBillableQty);
  if (base === null || mult === null || qty === null) return null;
  if (!Number.isInteger(creditsPerCurrency) || creditsPerCurrency <= 0) return null;

  // Product is scaled by 10^36; multiply by 10^6 then divide by 10^36 with half-up rounding
  const numerator = base * mult * qty * BigInt(creditsPerCurrency) * LEDGER_UNITS_PER_CREDIT;
  const divisor = SCALE_12 * SCALE_12 * SCALE_12;
  return (numerator + divisor / 2n) / divisor;
}

export function computeUnitLedgerUnits({ baseAmountDecimal, unihubMultiplier, creditsPerCurrency }) {
  return computeMinLedgerUnits({ baseAmountDecimal, unihubMultiplier, creditsPerCurrency, minBillableQty: '1' });
}

export function isValidAccountKind(value) {
  return ACCOUNT_KINDS.has(value);
}

function cleanText(value, maxLen) {
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  if (trimmed.length > maxLen || trimmed.includes('\0')) return null;
  return trimmed;
}

export function validateDisplayJson(display) {
  if (!display || typeof display !== 'object' || Array.isArray(display)) return null;
  const name = cleanText(display.name, 120);
  const description = cleanText(display.description ?? '', 600);
  if (!name || description === null) return null;
  if (!Array.isArray(display.benefits) || display.benefits.length > 20) return null;
  const benefits = [];
  for (const item of display.benefits) {
    const line = cleanText(item, 200);
    if (!line) return null;
    benefits.push(line);
  }
  return { name, description, benefits };
}

export function validateProductRule(productType, rule) {
  if (!PRODUCT_TYPES.has(productType) || !rule || typeof rule !== 'object' || Array.isArray(rule)) return null;
  const priceMinor = rule.priceMinor;
  if (!Number.isSafeInteger(priceMinor) || priceMinor < 0) return null;

  const packEligibility = rule.packEligibility ?? 'active_member';
  if (!PACK_ELIGIBILITIES.has(packEligibility)) return null;

  if (productType === 'credit_pack') {
    const { packCredits, validDays } = rule;
    if (!Number.isSafeInteger(packCredits) || packCredits <= 0) return null;
    if (!Number.isSafeInteger(validDays) || validDays < 1 || validDays > 3650) return null;
    return { priceMinor, packCredits, validDays, packEligibility };
  }

  const { billingCycle, baseCredits, bonusCredits = 0, carryoverPolicy = 'expire_each_cycle', downgradePolicy = 'period_end', entitlementKeys = [] } = rule;
  if (!BILLING_CYCLES.has(billingCycle)) return null;
  if (!Number.isSafeInteger(baseCredits) || baseCredits < 0) return null;
  if (!Number.isSafeInteger(bonusCredits) || bonusCredits < 0) return null;
  if (!CARRYOVER_POLICIES.has(carryoverPolicy)) return null;
  if (!DOWNGRADE_POLICIES.has(downgradePolicy)) return null;
  if (!Array.isArray(entitlementKeys) || entitlementKeys.length > 32) return null;
  const normalizedKeys = [];
  for (const entry of entitlementKeys) {
    if (typeof entry !== 'string' || !ENTITLEMENT_KEY_RE.test(entry.trim())) return null;
    normalizedKeys.push(entry.trim());
  }
  return {
    billingCycle,
    priceMinor,
    baseCredits,
    bonusCredits,
    carryoverPolicy,
    downgradePolicy,
    packEligibility,
    entitlementKeys: [...new Set(normalizedKeys)],
  };
}

export function validateDraftInput(body) {
  if (!body || typeof body !== 'object') return null;
  const { accountKind, productType, tierId, tierRank, rule, display } = body;
  if (!isValidAccountKind(accountKind) || !PRODUCT_TYPES.has(productType)) return null;
  if (typeof tierId !== 'string' || !TIER_ID_RE.test(tierId.trim())) return null;
  if (!Number.isSafeInteger(tierRank) || tierRank < 0 || tierRank > 100_000) return null;
  const validRule = validateProductRule(productType, rule);
  const validDisplay = validateDisplayJson(display);
  if (!validRule || !validDisplay) return null;
  return {
    accountKind,
    productType,
    tierId: tierId.trim(),
    tierRank,
    rule: validRule,
    display: validDisplay,
  };
}

export function parseEffectiveAt(value) {
  if (value === undefined || value === null || value === '') return new Date();
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

export function validateRoutePricingPublishInput(body) {
  if (!body || typeof body !== 'object') return { error: 'invalid_pricing' };
  const { accountKind, creditsPerCurrency, fxVersion = 'fx_native_v1', effectiveAt, routes } = body;
  if (!isValidAccountKind(accountKind)) return { error: 'invalid_account_kind' };
  if (!Number.isSafeInteger(creditsPerCurrency) || creditsPerCurrency <= 0 || creditsPerCurrency > 1_000_000) {
    return { error: 'invalid_credits_per_currency' };
  }
  const cleanFx = cleanText(fxVersion, 64);
  if (!cleanFx) return { error: 'invalid_fx_version' };
  const effectiveDate = parseEffectiveAt(effectiveAt);
  if (!effectiveDate) return { error: 'invalid_effective_at' };
  if (!Array.isArray(routes) || routes.length === 0 || routes.length > 200) return { error: 'invalid_routes' };

  const seenRouteIds = new Set();
  const normalizedRoutes = [];
  for (const item of routes) {
    if (!item || typeof item !== 'object') return { error: 'invalid_routes' };
    const modelId = cleanText(item.modelId, 128);
    const routeId = cleanText(item.routeId, 128);
    const operation = cleanText(item.operation ?? 'inference', 64);
    const unit = cleanText(item.unit, 64);
    const meterVersion = cleanText(item.meterVersion, 64);
    const routerRateVersion = cleanText(item.routerRateVersion, 64);
    const enabled = item.enabled !== false;
    if (!modelId || !routeId || !operation || !unit || !meterVersion || !routerRateVersion) {
      return { error: 'invalid_routes' };
    }
    if (seenRouteIds.has(routeId)) return { error: 'duplicate_route_id', routeId };
    seenRouteIds.add(routeId);

    const baseScaled = parseDecimalScaled12(item.baseAmountDecimal);
    const multScaled = parseDecimalScaled12(item.unihubMultiplier);
    const minQtyScaled = parseDecimalScaled12(item.minBillableQty ?? '1');
    if (baseScaled === null || baseScaled <= 0n || multScaled === null || minQtyScaled === null || minQtyScaled <= 0n) {
      return { error: 'invalid_route_pricing', routeId };
    }

    let entitlementFilter = null;
    if (item.entitlementFilter !== undefined && item.entitlementFilter !== null && item.entitlementFilter !== '') {
      if (typeof item.entitlementFilter !== 'string' || !ENTITLEMENT_KEY_RE.test(item.entitlementFilter.trim())) {
        return { error: 'invalid_entitlement_filter', routeId };
      }
      entitlementFilter = item.entitlementFilter.trim();
    }

    if (enabled) {
      if (multScaled <= 0n) return { error: 'granularity_underflow', routeId };
      const minUnits = computeMinLedgerUnits({
        baseAmountDecimal: String(item.baseAmountDecimal),
        unihubMultiplier: String(item.unihubMultiplier),
        creditsPerCurrency,
        minBillableQty: String(item.minBillableQty ?? '1'),
      });
      if (minUnits === null || minUnits < 1n) {
        return { error: 'granularity_underflow', routeId };
      }
      const unitUnits = computeUnitLedgerUnits({
        baseAmountDecimal: String(item.baseAmountDecimal),
        unihubMultiplier: String(item.unihubMultiplier),
        creditsPerCurrency,
      });
      normalizedRoutes.push({
        modelId,
        routeId,
        operation,
        unit,
        minBillableQty: String(item.minBillableQty ?? '1'),
        meterVersion,
        routerRateVersion,
        baseAmountDecimal: String(item.baseAmountDecimal),
        unihubMultiplier: String(item.unihubMultiplier),
        unitLedgerUnits: unitUnits.toString(),
        minLedgerUnits: minUnits.toString(),
        entitlementFilter,
      });
    }
  }

  if (normalizedRoutes.length === 0) return { error: 'no_enabled_routes' };
  return {
    value: {
      accountKind,
      creditsPerCurrency,
      fxVersion: cleanFx,
      effectiveAt: effectiveDate,
      routes: normalizedRoutes,
    },
  };
}
