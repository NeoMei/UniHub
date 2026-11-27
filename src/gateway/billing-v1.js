import { randomUUID, timingSafeEqual } from 'node:crypto';
import { closeHold } from '../billing/credits.js';
import { BillingError, sendBillingError } from '../billing/errors.js';
import { allocateCreditUnits } from '../billing/validation.js';
import { resolveCatalogModels } from '../commerce/catalog.js';
import { LEDGER_UNITS_PER_CREDIT, parseDecimalScaled12 } from '../commerce/validation.js';
import { resolveRoutePricingVersion } from '../commerce/versions.js';
import { isUuid, transaction } from '../database.js';
import { resolveModelKey } from './keys.js';

const SCALE_12 = 1_000_000_000_000n;
const ALLOWED_DEFAULT_VERSIONS = new Set(['1.0', '1.1']);

function safeTokenMatch(provided, expected) {
  if (typeof provided !== 'string' || typeof expected !== 'string' || !expected) return false;
  const a = Buffer.from(provided, 'utf8');
  const b = Buffer.from(expected, 'utf8');
  return a.length === b.length && timingSafeEqual(a, b);
}

function cleanId(value, max = 160) {
  return typeof value === 'string' && /^[A-Za-z0-9._:/-]{1,160}$/.test(value) && value.length <= max
    ? value : null;
}

export function computeTwoLayerUnits({
  baseAmount,
  providerCurrency,
  regionCurrency,
  fxRates = null,
  fxVersion = null,
  unihubMultiplier,
  creditsPerCurrency,
  roundingMode = 'ceil',
}) {
  const baseScaled = parseDecimalScaled12(baseAmount);
  const multScaled = parseDecimalScaled12(unihubMultiplier);
  if (baseScaled === null || multScaled === null || !Number.isInteger(creditsPerCurrency) || creditsPerCurrency <= 0) {
    throw new BillingError('invalid_pricing_input', 400);
  }

  let fxScaled = SCALE_12;
  let usedFxVersion = fxVersion ?? 'native';
  if (providerCurrency !== regionCurrency) {
    const rate = fxRates?.[providerCurrency];
    const parsedRate = parseDecimalScaled12(rate);
    if (parsedRate === null || parsedRate <= 0n || !fxVersion) {
      throw new BillingError('fx_rule_missing', 400, { providerCurrency, regionCurrency });
    }
    fxScaled = parsedRate;
    usedFxVersion = fxVersion;
  }

  // Scaled by 10^36 -> * creditsPerCurrency * 10^6, then divide by 10^36 with ceil or half-up rounding once
  const numerator = baseScaled * fxScaled * multScaled * BigInt(creditsPerCurrency) * LEDGER_UNITS_PER_CREDIT;
  const divisor = SCALE_12 * SCALE_12 * SCALE_12;
  const units = roundingMode === 'round'
    ? (numerator + divisor / 2n) / divisor
    : (numerator + divisor - 1n) / divisor;
  return { units, fxVersion: usedFxVersion };
}

function mapHoldRow(row) {
  return {
    id: row.id,
    accountId: row.account_id,
    appId: row.app_id,
    userId: row.user_id,
    requestId: row.request_id,
    status: row.status,
    authorizedUnits: BigInt(row.authorized_units).toString(),
    settledUnits: BigInt(row.settled_units).toString(),
    releasedUnits: BigInt(row.released_units).toString(),
    commitAttempt: row.commit_attempt ?? 0,
    committedBy: row.committed_by ?? null,
    committedAt: row.committed_at ?? null,
    upstreamRef: row.upstream_ref ?? null,
    expiresAt: row.expires_at,
    reconcileDeadline: row.reconcile_deadline ?? null,
    pricingSnapshot: row.pricing_snapshot,
    officialAmount: row.official_amount_json ?? null,
    baseAmount: row.base_amount_json ?? null,
    actualUsage: row.actual_usage_json ?? null,
    settledAt: row.settled_at,
  };
}

export async function reconcileExpiredHolds(pool, { now = new Date(), evidenceLookup = null } = {}) {
  const autoReleased = [];
  const extendedToReconciling = [];
  const settledFromEvidence = [];
  const timedOutDifferences = [];

  const { rows: expiredHolds } = await pool.query(`
    SELECT id FROM credit_holds
    WHERE status IN ('reserved', 'dispatching', 'in_flight', 'reconciling')
      AND expires_at <= $1
    ORDER BY expires_at ASC, id ASC`, [now]);

  for (const { id: holdId } of expiredHolds) {
    await transaction(pool, async (connection) => {
      const { rows: [hold] } = await connection.query(
        'SELECT * FROM credit_holds WHERE id = $1 FOR UPDATE', [holdId],
      );
      if (!hold || !['reserved', 'dispatching', 'in_flight', 'reconciling'].includes(hold.status)) return;
      if (new Date(hold.expires_at) > now) return;

      // Case 1: Never acquired commit authority -> auto-release cleanly
      if (hold.status === 'reserved' && (hold.commit_attempt ?? 0) === 0) {
        await closeHold(connection, hold.id, 0n, 'release');
        autoReleased.push(hold.id);
        return;
      }

      const evidence = evidenceLookup ? await evidenceLookup(mapHoldRow(hold)) : null;
      if (evidence?.status === 'settled') {
        const requested = BigInt(evidence.settledUnits);
        await closeHold(connection, hold.id, requested, 'settle');
        settledFromEvidence.push(hold.id);
        return;
      }
      if (evidence?.status === 'released') {
        await closeHold(connection, hold.id, 0n, 'release');
        autoReleased.push(hold.id);
        return;
      }

      const deadline = hold.reconcile_deadline ? new Date(hold.reconcile_deadline) : now;
      if (now < deadline) {
        const nextCheck = evidence?.nextCheckAt
          ? new Date(evidence.nextCheckAt)
          : new Date(Math.min(deadline.getTime(), now.getTime() + 5 * 60_000));
        await connection.query(`
          UPDATE credit_holds SET status = 'reconciling', expires_at = $1
          WHERE id = $2`, [nextCheck, hold.id]);
        extendedToReconciling.push(hold.id);
        return;
      }

      // Past reconcile_deadline: settle verified units (or 0n -> release) and log platform_cost_differences
      const verifiedUnits = evidence?.verifiedUnits !== undefined ? BigInt(evidence.verifiedUnits) : 0n;
      const authorizedUnits = BigInt(hold.authorized_units);
      const mode = verifiedUnits > 0n ? 'settle' : 'release';
      await closeHold(connection, hold.id, verifiedUnits, mode);

      const settled = verifiedUnits > authorizedUnits ? authorizedUnits : verifiedUnits;
      const diffUnits = authorizedUnits - settled;
      await connection.query(`
        INSERT INTO platform_cost_differences (
          id, hold_id, account_id, app_id, user_id, region, currency, reason,
          expected_units, settled_units, diff_units, details_json
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, 'reconcile_timeout', $8, $9, $10, $11::jsonb)
        ON CONFLICT (hold_id, reason) DO NOTHING`, [
        randomUUID(), hold.id, hold.account_id, hold.app_id, hold.user_id,
        hold.region ?? 'cn', hold.currency ?? 'CNY',
        authorizedUnits.toString(), settled.toString(), diffUnits.toString(),
        JSON.stringify({ reconcileDeadline: deadline.toISOString(), statusBefore: hold.status }),
      ]);
      timedOutDifferences.push(hold.id);
    });
  }

  return { autoReleased, extendedToReconciling, settledFromEvidence, timedOutDifferences };
}

export function registerGatewayBillingRoutes(app, pool, {
  region,
  currency,
  gatewayServiceToken = '',
  allowedGatewayVersions = ALLOWED_DEFAULT_VERSIONS,
  fxRates = null,
}) {
  function verifyGatewayAuth(request, reply) {
    if (!gatewayServiceToken) {
      return reply.code(503).send({ error: 'gateway_service_unconfigured' });
    }
    const headerToken = request.headers['x-unihub-service-token']
      ?? (typeof request.headers.authorization === 'string' && request.headers.authorization.startsWith('Bearer ')
        ? request.headers.authorization.slice(7) : '');
    if (!safeTokenMatch(headerToken, gatewayServiceToken)) {
      return reply.code(401).send({ error: 'invalid_service_token' });
    }
    const gwVersion = request.headers['x-gateway-version'] ?? request.body?.gatewayVersion;
    if (!gwVersion || !allowedGatewayVersions.has(String(gwVersion))) {
      return reply.code(400).send({ error: 'unsupported_gateway_version' });
    }
    const reqRegion = request.headers['x-unihub-region'] ?? request.body?.region;
    const reqCurrency = request.headers['x-unihub-currency'] ?? request.body?.currency;
    if ((reqRegion && reqRegion !== region) || (reqCurrency && reqCurrency !== currency)) {
      return reply.code(400).send({ error: 'region_currency_mismatch' });
    }
    return null;
  }

  const gwOpts = {
    bodyLimit: 65_536,
    preHandler: async (request, reply) => verifyGatewayAuth(request, reply),
  };

  app.post('/internal/billing/v1/catalog', gwOpts, async (request, reply) => {
    try {
      const body = request.body ?? {};
      const keyCtx = await resolveModelKey(pool, body.apiKey, { region, currency });
      return resolveCatalogModels(pool, {
        appId: keyCtx.appId,
        accountId: keyCtx.accountId,
        accountKind: keyCtx.accountKind,
        userId: keyCtx.userId,
        region,
        entitlements: Array.isArray(body.entitlements) ? body.entitlements : null,
        activeRouterRouteIds: Array.isArray(body.activeRouterRouteIds) ? body.activeRouterRouteIds : null,
      });
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/internal/billing/v1/holds', gwOpts, async (request, reply) => {
    try {
      const body = request.body ?? {};
      const requestId = cleanId(body.requestId);
      const routeId = cleanId(body.routeId);
      const meterVersion = cleanId(body.meterVersion);
      const routerRateVersion = cleanId(body.routerRateVersion);
      const providerCurrency = cleanId(body.providerCurrency ?? currency, 12);
      const ttlSeconds = Number.isSafeInteger(body.ttlSeconds) && body.ttlSeconds > 0
        ? body.ttlSeconds : 300;
      const reconcileTtlSeconds = Number.isSafeInteger(body.reconcileTtlSeconds) && body.reconcileTtlSeconds >= ttlSeconds
        ? body.reconcileTtlSeconds : 3600;

      if (!requestId || !routeId || !meterVersion || !routerRateVersion || !providerCurrency) {
        throw new BillingError('invalid_billing_hold', 400);
      }

      const keyCtx = await resolveModelKey(pool, body.apiKey, { region, currency });
      const gatewayVersion = String(request.headers['x-gateway-version'] ?? body.gatewayVersion);

      const pricingVersion = await resolveRoutePricingVersion(pool, {
        appId: keyCtx.appId, accountKind: keyCtx.accountKind, region, at: new Date(),
      });
      if (!pricingVersion) throw new BillingError('route_pricing_not_published', 404);
      const route = pricingVersion.routesSnapshot.find((r) => r.routeId === routeId);
      if (!route) throw new BillingError('route_not_allowed', 403);
      if (route.meterVersion !== meterVersion || route.routerRateVersion !== routerRateVersion) {
        throw new BillingError('pricing_version_mismatch', 409);
      }

      const { units: authorizedUnits, fxVersion } = computeTwoLayerUnits({
        baseAmount: body.baseAmount,
        providerCurrency,
        regionCurrency: currency,
        fxRates,
        fxVersion: body.fxVersion ?? pricingVersion.fxVersion,
        unihubMultiplier: route.unihubMultiplier,
        creditsPerCurrency: pricingVersion.creditsPerCurrency,
        roundingMode: 'ceil',
      });
      if (authorizedUnits <= 0n) throw new BillingError('zero_hold_not_allowed', 400);

      const snapshot = {
        catalogVersionId: pricingVersion.id,
        catalogVersionCode: pricingVersion.versionCode,
        retailRateVersion: pricingVersion.versionCode,
        modelId: route.modelId,
        routeId: route.routeId,
        operation: route.operation,
        meterVersion: route.meterVersion,
        routerRateVersion: route.routerRateVersion,
        providerCurrency,
        regionCurrency: currency,
        fxVersion,
        unihubMultiplier: route.unihubMultiplier,
        creditsPerCurrency: pricingVersion.creditsPerCurrency,
        officialAmount: body.officialAmount ?? null,
        baseAmount: String(body.baseAmount),
        items: Array.isArray(body.items) ? body.items : [],
      };

      const result = await transaction(pool, async (connection) => {
        const { rows: [existing] } = await connection.query(
          'SELECT * FROM credit_holds WHERE account_id = $1 AND request_id = $2 FOR UPDATE',
          [keyCtx.accountId, requestId],
        );
        if (existing) {
          if (existing.app_id !== keyCtx.appId || existing.user_id !== keyCtx.userId
            || BigInt(existing.authorized_units) !== authorizedUnits) {
            throw new BillingError('credit_hold_conflict', 409);
          }
          return { hold: mapHoldRow(existing), duplicate: true };
        }

        const { rows: [wallet] } = await connection.query(
          'SELECT * FROM wallet_accounts WHERE account_id = $1 FOR UPDATE', [keyCtx.accountId],
        );
        if (!wallet || BigInt(wallet.arrears_minor) > 0n || wallet.frozen_reason) {
          throw new BillingError('wallet_frozen', 409);
        }

        let cycle = null;
        if (keyCtx.accountKind === 'enterprise') {
          const { rows: [sub] } = await connection.query(`
            SELECT s.id AS "subscriptionId", s.current_grant_cycle_index AS "cycleIndex"
            FROM enterprise_app_seats e
            JOIN app_subscriptions s ON s.id = e.subscription_id AND s.status = 'active'
            WHERE e.account_id = $1 AND e.app_id = $2 AND e.user_id = $3
            FOR SHARE OF s`, [keyCtx.accountId, keyCtx.appId, keyCtx.userId]);
          if (!sub) throw new BillingError('app_seat_required', 403);

          const { rows: [limit] } = await connection.query(`
            SELECT cycle_limit_units FROM enterprise_member_limits
            WHERE account_id = $1 AND app_id = $2 AND user_id = $3`,
          [keyCtx.accountId, keyCtx.appId, keyCtx.userId]);
          const { rows: [usage] } = await connection.query(`
            SELECT held_units, settled_units FROM enterprise_member_cycle_usage
            WHERE account_id = $1 AND app_id = $2 AND user_id = $3
              AND subscription_id = $4 AND cycle_index = $5
            FOR UPDATE`, [keyCtx.accountId, keyCtx.appId, keyCtx.userId, sub.subscriptionId, sub.cycleIndex]);
          const limitUnits = limit?.cycle_limit_units == null ? null : BigInt(limit.cycle_limit_units);
          const heldUnits = usage ? BigInt(usage.held_units) : 0n;
          const settledUnits = usage ? BigInt(usage.settled_units) : 0n;
          if (limitUnits !== null && heldUnits + settledUnits + authorizedUnits > limitUnits) {
            throw new BillingError('member_cycle_limit', 409);
          }
          cycle = { subscriptionId: sub.subscriptionId, cycleIndex: Number(sub.cycleIndex) };
        }

        const holdId = randomUUID();
        const { rows: [created] } = await connection.query(`
          INSERT INTO credit_holds (
            id, account_id, app_id, user_id, enterprise_subscription_id, enterprise_cycle_index,
            request_id, status, authorized_units, pricing_snapshot,
            expires_at, reconcile_deadline, region, currency, gateway_version,
            official_amount_json, base_amount_json
          ) VALUES (
            $1, $2, $3, $4, $5, $6,
            $7, 'reserved', $8, $9::jsonb,
            statement_timestamp() + ($10::integer * interval '1 second'),
            statement_timestamp() + ($11::integer * interval '1 second'),
            $12, $13, $14, $15::jsonb, $16::jsonb
          ) RETURNING *`, [
          holdId, keyCtx.accountId, keyCtx.appId, keyCtx.userId,
          cycle?.subscriptionId ?? null, cycle?.cycleIndex ?? null,
          requestId, authorizedUnits.toString(), JSON.stringify(snapshot),
          ttlSeconds, reconcileTtlSeconds, region, currency, gatewayVersion,
          JSON.stringify(body.officialAmount ?? null),
          JSON.stringify({ amount: String(body.baseAmount), currency: providerCurrency }),
        ]);

        const { rows: batches } = await connection.query(`
          SELECT id, source_type AS "sourceType", expires_at AS "expiresAt", available_units AS "availableUnits"
          FROM credit_batches
          WHERE account_id = $1 AND app_id = $2 AND available_units > 0
            AND NOT frozen_for_new_holds AND expires_at > statement_timestamp()
          ORDER BY expires_at ASC,
            CASE source_type WHEN 'plan_base' THEN 0 WHEN 'credit_pack' THEN 1 ELSE 2 END ASC,
            id ASC
          FOR UPDATE`, [keyCtx.accountId, keyCtx.appId]);

        let allocation;
        try {
          allocation = allocateCreditUnits(batches, authorizedUnits);
        } catch {
          throw new BillingError('insufficient_credits', 402, {
            requiredUnits: authorizedUnits.toString(),
          });
        }

        for (const item of allocation) {
          await connection.query(`
            UPDATE credit_batches
            SET available_units = available_units - $1, held_units = held_units + $1
            WHERE id = $2`, [item.units.toString(), item.batchId]);
          await connection.query(`
            INSERT INTO credit_hold_batches (hold_id, batch_id, held_units)
            VALUES ($1, $2, $3)`, [holdId, item.batchId, item.units.toString()]);
          await connection.query(`
            INSERT INTO credit_ledger_entries (
              id, batch_id, account_id, app_id, entry_type, delta_available_units,
              delta_held_units, delta_settled_units, delta_expired_units, ref_id, idempotency_key
            ) VALUES ($1, $2, $3, $4, 'hold', $5, $6, 0, 0, $7, $8)`, [
            randomUUID(), item.batchId, keyCtx.accountId, keyCtx.appId,
            (-item.units).toString(), item.units.toString(), holdId, `hold:${holdId}:${item.batchId}`,
          ]);
        }

        if (cycle) {
          await connection.query(`
            INSERT INTO enterprise_member_cycle_usage (
              account_id, app_id, user_id, subscription_id, cycle_index, held_units
            ) VALUES ($1, $2, $3, $4, $5, $6)
            ON CONFLICT (account_id, app_id, user_id, subscription_id, cycle_index)
            DO UPDATE SET held_units = enterprise_member_cycle_usage.held_units + EXCLUDED.held_units`, [
            keyCtx.accountId, keyCtx.appId, keyCtx.userId,
            cycle.subscriptionId, cycle.cycleIndex, authorizedUnits.toString(),
          ]);
        }

        return { hold: mapHoldRow(created), duplicate: false };
      });

      return reply.code(result.duplicate ? 200 : 201).send(result);
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/internal/billing/v1/holds/:holdId/commit', gwOpts, async (request, reply) => {
    try {
      const { holdId } = request.params;
      const body = request.body ?? {};
      const executorId = cleanId(body.executorId ?? 'router-main', 120);
      const attemptNo = Number.isSafeInteger(body.attempt) && body.attempt >= 1 ? body.attempt : 1;
      const upstreamRef = body.upstreamRef ? cleanId(body.upstreamRef, 200) : null;
      const targetStatus = body.state === 'in_flight' || upstreamRef ? 'in_flight' : 'dispatching';
      if (!isUuid(holdId) || !executorId) throw new BillingError('invalid_commit_request', 400);

      const hold = await transaction(pool, async (connection) => {
        const { rows: [existing] } = await connection.query(
          'SELECT * FROM credit_holds WHERE id = $1 FOR UPDATE', [holdId],
        );
        if (!existing) throw new BillingError('credit_hold_not_found', 404);
        if (existing.status === 'settled' || existing.status === 'released') {
          throw new BillingError('hold_already_closed', 409);
        }
        if (existing.commit_attempt > 0 && attemptNo <= existing.commit_attempt && existing.committed_by !== executorId) {
          throw new BillingError('commit_conflict', 409);
        }
        if (attemptNo < existing.commit_attempt) {
          throw new BillingError('stale_commit_attempt', 409);
        }

        const { rows: [updated] } = await connection.query(`
          UPDATE credit_holds
          SET status = $1,
              commit_attempt = $2,
              committed_by = $3,
              committed_at = COALESCE(committed_at, statement_timestamp()),
              upstream_ref = COALESCE($4, upstream_ref)
          WHERE id = $5
          RETURNING *`, [targetStatus, attemptNo, executorId, upstreamRef, holdId]);
        return mapHoldRow(updated);
      });
      return { hold };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/internal/billing/v1/holds/:holdId/extend', gwOpts, async (request, reply) => {
    try {
      const { holdId } = request.params;
      const body = request.body ?? {};
      if (!isUuid(holdId)) throw new BillingError('invalid_credit_hold', 400);
      const keyCtx = await resolveModelKey(pool, body.apiKey, { region, currency });

      const result = await transaction(pool, async (connection) => {
        const { rows: [hold] } = await connection.query(
          'SELECT * FROM credit_holds WHERE id = $1 FOR UPDATE', [holdId],
        );
        if (!hold) throw new BillingError('credit_hold_not_found', 404);
        if (hold.account_id !== keyCtx.accountId || hold.app_id !== keyCtx.appId || hold.user_id !== keyCtx.userId) {
          throw new BillingError('account_forbidden', 403);
        }
        if (hold.status === 'settled' || hold.status === 'released') {
          throw new BillingError('hold_already_closed', 409);
        }

        const snap = hold.pricing_snapshot;
        const { units: extraUnits } = computeTwoLayerUnits({
          baseAmount: body.additionalBaseAmount,
          providerCurrency: snap.providerCurrency ?? currency,
          regionCurrency: currency,
          fxRates,
          fxVersion: snap.fxVersion,
          unihubMultiplier: snap.unihubMultiplier,
          creditsPerCurrency: Number(snap.creditsPerCurrency),
          roundingMode: 'ceil',
        });
        if (extraUnits <= 0n) throw new BillingError('invalid_extend_units', 400);

        const { rows: [wallet] } = await connection.query(
          'SELECT * FROM wallet_accounts WHERE account_id = $1 FOR UPDATE', [hold.account_id],
        );
        if (!wallet || BigInt(wallet.arrears_minor) > 0n || wallet.frozen_reason) {
          throw new BillingError('wallet_frozen', 409);
        }

        if (hold.enterprise_subscription_id && hold.enterprise_cycle_index !== null) {
          const { rows: [limit] } = await connection.query(`
            SELECT cycle_limit_units FROM enterprise_member_limits
            WHERE account_id = $1 AND app_id = $2 AND user_id = $3`, [hold.account_id, hold.app_id, hold.user_id]);
          const { rows: [usage] } = await connection.query(`
            SELECT held_units, settled_units FROM enterprise_member_cycle_usage
            WHERE account_id = $1 AND app_id = $2 AND user_id = $3
              AND subscription_id = $4 AND cycle_index = $5
            FOR UPDATE`, [hold.account_id, hold.app_id, hold.user_id, hold.enterprise_subscription_id, hold.enterprise_cycle_index]);
          const limitUnits = limit?.cycle_limit_units == null ? null : BigInt(limit.cycle_limit_units);
          const currentUsed = (usage ? BigInt(usage.held_units) + BigInt(usage.settled_units) : 0n);
          if (limitUnits !== null && currentUsed + extraUnits > limitUnits) {
            throw new BillingError('member_cycle_limit', 409);
          }
          await connection.query(`
            UPDATE enterprise_member_cycle_usage
            SET held_units = held_units + $1
            WHERE account_id = $2 AND app_id = $3 AND user_id = $4
              AND subscription_id = $5 AND cycle_index = $6`, [
            extraUnits.toString(), hold.account_id, hold.app_id, hold.user_id,
            hold.enterprise_subscription_id, hold.enterprise_cycle_index,
          ]);
        }

        const { rows: batches } = await connection.query(`
          SELECT id, source_type AS "sourceType", expires_at AS "expiresAt", available_units AS "availableUnits"
          FROM credit_batches
          WHERE account_id = $1 AND app_id = $2 AND available_units > 0
            AND NOT frozen_for_new_holds AND expires_at > statement_timestamp()
          ORDER BY expires_at ASC,
            CASE source_type WHEN 'plan_base' THEN 0 WHEN 'credit_pack' THEN 1 ELSE 2 END ASC,
            id ASC
          FOR UPDATE`, [hold.account_id, hold.app_id]);

        let allocation;
        try {
          allocation = allocateCreditUnits(batches, extraUnits);
        } catch {
          throw new BillingError('insufficient_credits', 402);
        }

        for (const item of allocation) {
          await connection.query(`
            UPDATE credit_batches
            SET available_units = available_units - $1, held_units = held_units + $1
            WHERE id = $2`, [item.units.toString(), item.batchId]);
          await connection.query(`
            INSERT INTO credit_hold_batches (hold_id, batch_id, held_units)
            VALUES ($1, $2, $3)
            ON CONFLICT (hold_id, batch_id)
            DO UPDATE SET held_units = credit_hold_batches.held_units + EXCLUDED.held_units`, [
            hold.id, item.batchId, item.units.toString(),
          ]);
          await connection.query(`
            INSERT INTO credit_ledger_entries (
              id, batch_id, account_id, app_id, entry_type, delta_available_units,
              delta_held_units, delta_settled_units, delta_expired_units, ref_id, idempotency_key
            ) VALUES ($1, $2, $3, $4, 'hold', $5, $6, 0, 0, $7, $8)`, [
            randomUUID(), item.batchId, hold.account_id, hold.app_id,
            (-item.units).toString(), item.units.toString(), hold.id,
            `extend:${hold.id}:${randomUUID()}`,
          ]);
        }

        const { rows: [updated] } = await connection.query(`
          UPDATE credit_holds
          SET authorized_units = authorized_units + $1,
              expires_at = GREATEST(expires_at, statement_timestamp() + interval '5 minutes')
          WHERE id = $2 RETURNING *`, [extraUnits.toString(), hold.id]);
        return { hold: mapHoldRow(updated), extendedUnits: extraUnits.toString() };
      });
      return result;
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/internal/billing/v1/holds/:holdId/settle', gwOpts, async (request, reply) => {
    try {
      const { holdId } = request.params;
      const body = request.body ?? {};
      if (!isUuid(holdId)) throw new BillingError('invalid_credit_hold', 400);

      const settled = await transaction(pool, async (connection) => {
        const { rows: [hold] } = await connection.query(
          'SELECT * FROM credit_holds WHERE id = $1 FOR UPDATE', [holdId],
        );
        if (!hold) throw new BillingError('credit_hold_not_found', 404);
        if (hold.status === 'settled' || hold.status === 'released') {
          return { hold: mapHoldRow(hold), duplicate: true, platformDifferenceUnits: '0' };
        }

        const snap = hold.pricing_snapshot;
        let actualUnits;
        if (body.actualUnits !== undefined) {
          actualUnits = BigInt(body.actualUnits);
        } else {
          const calc = computeTwoLayerUnits({
            baseAmount: body.baseAmount,
            providerCurrency: body.providerCurrency ?? snap.providerCurrency ?? currency,
            regionCurrency: currency,
            fxRates,
            fxVersion: snap.fxVersion,
            unihubMultiplier: snap.unihubMultiplier,
            creditsPerCurrency: Number(snap.creditsPerCurrency),
            roundingMode: 'round',
          });
          actualUnits = calc.units;
        }
        if (actualUnits < 0n) throw new BillingError('invalid_settlement_units', 400);

        const closeResult = await closeHold(connection, hold.id, actualUnits, actualUnits === 0n ? 'release' : 'settle');
        await connection.query(`
          UPDATE credit_holds
          SET official_amount_json = COALESCE($1::jsonb, official_amount_json),
              base_amount_json = COALESCE($2::jsonb, base_amount_json),
              actual_usage_json = COALESCE($3::jsonb, actual_usage_json),
              upstream_ref = COALESCE($4, upstream_ref)
          WHERE id = $5`, [
          body.officialAmount !== undefined ? JSON.stringify(body.officialAmount) : null,
          body.baseAmount !== undefined ? JSON.stringify({ amount: String(body.baseAmount), currency: body.providerCurrency ?? snap.providerCurrency ?? currency }) : null,
          body.actualUsage !== undefined ? JSON.stringify(body.actualUsage) : null,
          body.upstreamRef ?? null,
          hold.id,
        ]);

        const authorizedUnits = BigInt(hold.authorized_units);
        if (actualUnits > authorizedUnits) {
          const diffUnits = actualUnits - authorizedUnits;
          await connection.query(`
            INSERT INTO platform_cost_differences (
              id, hold_id, account_id, app_id, user_id, region, currency, reason,
              expected_units, settled_units, diff_units, details_json
            ) VALUES ($1, $2, $3, $4, $5, $6, $7, 'overage', $8, $9, $10, $11::jsonb)
            ON CONFLICT (hold_id, reason) DO NOTHING`, [
            randomUUID(), hold.id, hold.account_id, hold.app_id, hold.user_id,
            hold.region ?? region, hold.currency ?? currency,
            actualUnits.toString(), authorizedUnits.toString(), diffUnits.toString(),
            JSON.stringify({ upstreamRef: body.upstreamRef ?? hold.upstream_ref ?? null, routeId: snap.routeId }),
          ]);
        }

        const { rows: [refreshed] } = await connection.query('SELECT * FROM credit_holds WHERE id = $1', [hold.id]);
        return {
          hold: mapHoldRow(refreshed),
          duplicate: false,
          platformDifferenceUnits: closeResult.platformDifferenceUnits,
        };
      });
      return settled;
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/internal/billing/v1/holds/:holdId/release', gwOpts, async (request, reply) => {
    try {
      const { holdId } = request.params;
      if (!isUuid(holdId)) throw new BillingError('invalid_credit_hold', 400);
      return await transaction(pool, async (connection) => {
        const result = await closeHold(connection, holdId, 0n, 'release');
        const { rows: [refreshed] } = await connection.query('SELECT * FROM credit_holds WHERE id = $1', [holdId]);
        return { hold: mapHoldRow(refreshed), duplicate: result.duplicate };
      });
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.get('/internal/billing/v1/holds/:holdId', gwOpts, async (request, reply) => {
    try {
      const { holdId } = request.params;
      if (!isUuid(holdId)) throw new BillingError('invalid_credit_hold', 400);
      const { rows: [hold] } = await pool.query('SELECT * FROM credit_holds WHERE id = $1', [holdId]);
      if (!hold) throw new BillingError('credit_hold_not_found', 404);
      return { hold: mapHoldRow(hold) };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
}
