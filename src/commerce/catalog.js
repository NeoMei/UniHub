import { isUuid } from '../database.js';
import { LEDGER_UNITS_PER_CREDIT, parseEffectiveAt } from './validation.js';
import { resolveRoutePricingVersion } from './versions.js';
import { deepSeekProPrices } from './deepseek-pricing.js';

function formatCreditsFromUnits(unitsBigInt) {
  const whole = unitsBigInt / LEDGER_UNITS_PER_CREDIT;
  const frac = (unitsBigInt % LEDGER_UNITS_PER_CREDIT).toString().padStart(6, '0');
  const full = `${whole}.${frac}`;
  const num = Number(full);
  if (unitsBigInt > 0n && num < 0.01) return num.toFixed(4);
  return num.toFixed(2);
}

/**
 * Shared single-source catalog resolver used by both the Portal UI and
 * LLMRouter's /v1/models proxy (Stage 3).
 *
 * Intersects:
 *  1) Active published route_pricing_versions for (appId, accountKind, region, at)
 *  2) Enterprise per-app seat eligibility (if enterprise account)
 *  3) Optional user entitlement keys (filters routes with entitlementFilter)
 *  4) Optional active router route IDs reported by LLMRouter
 */
export async function resolveCatalogModels(pool, {
  appId, accountId, accountKind, userId, region, at = new Date(),
  entitlements = null, activeRouterRouteIds = null,
}) {
  const { rows: [appRow] } = await pool.query('SELECT id, name, enabled FROM apps WHERE id = $1', [appId]);
  if (!appRow || !appRow.enabled) return { error: 'app_unavailable' };

  // Per-app seat check for enterprise accounts: prefer Stage 2b active subscription seats,
  // falling back to Stage 2a seat contracts / legacy flag when no active subscription exists.
  let hasAppSeat = true;
  if (accountKind === 'enterprise') {
    const { rows: [activeSub] } = await pool.query(`
      SELECT id FROM app_subscriptions
      WHERE account_id = $1 AND app_id = $2 AND status = 'active'
      LIMIT 1`, [accountId, appId]);
    if (activeSub) {
      const { rowCount } = await pool.query(`
        SELECT 1 FROM enterprise_app_seats
        WHERE account_id = $1 AND app_id = $2 AND subscription_id = $3 AND user_id = $4`,
      [accountId, appId, activeSub.id, userId]);
      hasAppSeat = rowCount === 1;
    } else {
      const { rows: [seatCheck] } = await pool.query(`
        SELECT
          EXISTS (
            SELECT 1 FROM enterprise_app_seat_contracts
            WHERE account_id = $1 AND app_id = $2 AND user_id = $3 AND revoked_at IS NULL
          ) AS "hasAppContractSeat",
          EXISTS (
            SELECT 1 FROM enterprise_app_seat_contracts
            WHERE account_id = $1 AND app_id = $2
          ) AS "appHasAnyContract",
          COALESCE((
            SELECT has_seat FROM enterprise_members
            WHERE account_id = $1 AND user_id = $3
          ), false) AS "legacyHasSeat"`,
      [accountId, appId, userId]);
      hasAppSeat = seatCheck.appHasAnyContract ? seatCheck.hasAppContractSeat : seatCheck.legacyHasSeat;
    }
  }

  const pricingVersion = await resolveRoutePricingVersion(pool, { appId, accountKind, region, at });
  if (!pricingVersion) {
    return {
      app: { id: appRow.id, name: appRow.name },
      accountKind,
      region,
      hasAppSeat,
      pricingVersionId: null,
      pricingVersionCode: null,
      models: [],
    };
  }

  if (!hasAppSeat) {
    return {
      app: { id: appRow.id, name: appRow.name },
      accountKind,
      region,
      hasAppSeat: false,
      pricingVersionId: pricingVersion.id,
      pricingVersionCode: pricingVersion.versionCode,
      models: [],
    };
  }

  const entitlementSet = entitlements ? new Set(entitlements) : null;
  const activeRouteSet = activeRouterRouteIds ? new Set(activeRouterRouteIds) : null;

  const eligibleRoutes = pricingVersion.routesSnapshot.filter((route) => {
    if (activeRouteSet && !activeRouteSet.has(route.routeId)) return false;
    if (route.entitlementFilter && entitlementSet && !entitlementSet.has(route.entitlementFilter)) return false;
    return true;
  });

  // Group routes by modelId; display price range for same unit or itemized prices across units
  const byModel = new Map();
  for (const route of eligibleRoutes) {
    if (!byModel.has(route.modelId)) byModel.set(route.modelId, []);
    byModel.get(route.modelId).push(route);
  }

  const models = [];
  for (const [modelId, routes] of byModel.entries()) {
    const pricing = [];
    const unitsByUnit = new Map();
    for (const r of routes) {
      const componentPrices = deepSeekProPrices(r, pricingVersion);
      if (componentPrices) {
        pricing.push(...componentPrices);
        continue;
      }
      const u = BigInt(r.unitLedgerUnits);
      if (!unitsByUnit.has(r.unit)) unitsByUnit.set(r.unit, []);
      unitsByUnit.get(r.unit).push(u);
    }
    for (const [unit, values] of unitsByUnit.entries()) {
      values.sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
      const min = values[0];
      const max = values[values.length - 1];
      pricing.push({
        unit,
        minLedgerUnits: min.toString(),
        maxLedgerUnits: max.toString(),
        display: min === max
          ? `${formatCreditsFromUnits(min)} 积分 / ${unit}`
          : `${formatCreditsFromUnits(min)} ~ ${formatCreditsFromUnits(max)} 积分 / ${unit}`,
      });
    }
    models.push({
      modelId,
      operations: [...new Set(routes.map((r) => r.operation))],
      pricing,
      routes: routes.map((r) => ({
        routeId: r.routeId,
        operation: r.operation,
        unit: r.unit,
        meterVersion: r.meterVersion,
        routerRateVersion: r.routerRateVersion,
        unitLedgerUnits: r.unitLedgerUnits,
        minLedgerUnits: r.minLedgerUnits,
        entitlementFilter: r.entitlementFilter,
      })),
    });
  }

  return {
    app: { id: appRow.id, name: appRow.name },
    accountKind,
    region,
    currency: pricingVersion.currency,
    hasAppSeat: true,
    pricingVersionId: pricingVersion.id,
    pricingVersionCode: pricingVersion.versionCode,
    creditsPerCurrency: pricingVersion.creditsPerCurrency,
    models,
  };
}

export function registerCatalogRoutes(app, pool, auth, { region }) {
  app.get('/api/catalog/models', auth, async (request, reply) => {
    const { appId, at, entitlements, routerRoutes } = request.query ?? {};
    if (!isUuid(appId)) return reply.code(400).send({ error: 'invalid_app' });
    const atDate = parseEffectiveAt(at);
    if (!atDate) return reply.code(400).send({ error: 'invalid_effective_at' });

    const { rows: [account] } = await pool.query(`
      SELECT a.id, a.kind
      FROM accounts a
      LEFT JOIN enterprise_members m ON a.id = m.account_id AND m.user_id = $2
      WHERE a.id = $1 AND ((a.kind = 'personal' AND a.owner_id = $2) OR (a.kind = 'enterprise' AND m.user_id IS NOT NULL))`,
    [request.auth.activeAccountId, request.auth.userId]);
    if (!account) return reply.code(403).send({ error: 'account_forbidden' });

    const entList = typeof entitlements === 'string'
      ? entitlements.split(',').map((s) => s.trim()).filter(Boolean)
      : null;
    const routerRouteList = typeof routerRoutes === 'string'
      ? routerRoutes.split(',').map((s) => s.trim()).filter(Boolean)
      : null;

    const result = await resolveCatalogModels(pool, {
      appId,
      accountId: account.id,
      accountKind: account.kind,
      userId: request.auth.userId,
      region,
      at: atDate,
      entitlements: entList,
      activeRouterRouteIds: routerRouteList,
    });
    if (result.error) return reply.code(404).send({ error: result.error });
    return result;
  });
}
