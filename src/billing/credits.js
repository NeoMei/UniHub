import { randomUUID } from 'node:crypto';
import { transaction } from '../database.js';
import { BillingError } from './errors.js';
import { allocateCreditUnits, parseAmountMinor } from './validation.js';

const batchSettledHooks = [];

export function registerBatchSettledHook(hook) {
  if (typeof hook === 'function' && !batchSettledHooks.includes(hook)) {
    batchSettledHooks.push(hook);
  }
}

function cleanRequestId(value) {
  return typeof value === 'string' && /^[A-Za-z0-9._:-]{1,160}$/.test(value) ? value : null;
}

function serializeHold(row) {
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
    pricingSnapshot: row.pricing_snapshot,
    expiresAt: row.expires_at,
    createdAt: row.created_at,
    settledAt: row.settled_at,
  };
}

async function verifyConsumptionAccess(connection, { accountId, appId, userId }) {
  const { rows: [account] } = await connection.query(`
    SELECT a.kind, m.role, m.can_purchase
    FROM accounts a
    LEFT JOIN enterprise_members m ON m.account_id = a.id AND m.user_id = $2
    WHERE a.id = $1`, [accountId, userId]);
  if (!account) throw new BillingError('account_forbidden', 403);
  if (account.kind === 'personal') return { kind: 'personal', subscription: null };
  const { rows: [seat] } = await connection.query(`
    SELECT s.id AS "subscriptionId", s.current_grant_cycle_index AS "cycleIndex"
    FROM enterprise_app_seats e
    JOIN app_subscriptions s ON s.id = e.subscription_id AND s.status = 'active'
    WHERE e.account_id = $1 AND e.app_id = $2 AND e.user_id = $3
    LIMIT 1`, [accountId, appId, userId]);
  if (!seat) throw new BillingError('app_seat_required', 403);
  return { kind: 'enterprise', subscription: seat };
}

async function readHold(connection, holdId, forUpdate = false) {
  const { rows: [hold] } = await connection.query(
    `SELECT * FROM credit_holds WHERE id = $1${forUpdate ? ' FOR UPDATE' : ''}`, [holdId],
  );
  return hold;
}

export async function holdCredits(pool, {
  accountId, appId, userId, requestId: rawRequestId, authorizedUnits: rawUnits,
  pricingSnapshot,
}) {
  const requestId = cleanRequestId(rawRequestId);
  const authorizedUnits = parseAmountMinor(rawUnits);
  if (!requestId || !authorizedUnits || authorizedUnits <= 0n
    || !pricingSnapshot || typeof pricingSnapshot !== 'object' || Array.isArray(pricingSnapshot)) {
    throw new BillingError('invalid_credit_hold', 400);
  }

  return transaction(pool, async (connection) => {
    const { rows: [existing] } = await connection.query(`
      SELECT * FROM credit_holds WHERE account_id = $1 AND request_id = $2 FOR UPDATE`, [accountId, requestId]);
    if (existing) {
      if (existing.app_id !== appId || existing.user_id !== userId
        || BigInt(existing.authorized_units) !== authorizedUnits) {
        throw new BillingError('credit_hold_conflict', 409);
      }
      return { hold: serializeHold(existing), duplicate: true };
    }

    const access = await verifyConsumptionAccess(connection, { accountId, appId, userId });
    const { rows: [wallet] } = await connection.query(
      'SELECT * FROM wallet_accounts WHERE account_id = $1 FOR UPDATE', [accountId],
    );
    if (!wallet) throw new BillingError('wallet_not_found', 404);
    if (BigInt(wallet.arrears_minor) > 0n || wallet.frozen_reason) {
      throw new BillingError('wallet_frozen', 409);
    }

    let cycle = null;
    if (access.kind === 'enterprise') {
      const { rows: [limit] } = await connection.query(`
        SELECT cycle_limit_units FROM enterprise_member_limits
        WHERE account_id = $1 AND app_id = $2 AND user_id = $3`, [accountId, appId, userId]);
      const { rows: [usage] } = await connection.query(`
        SELECT held_units, settled_units FROM enterprise_member_cycle_usage
        WHERE account_id = $1 AND app_id = $2 AND user_id = $3
          AND subscription_id = $4 AND cycle_index = $5
        FOR UPDATE`, [accountId, appId, userId, access.subscription.subscriptionId, access.subscription.cycleIndex]);
      cycle = {
        subscriptionId: access.subscription.subscriptionId,
        cycleIndex: Number(access.subscription.cycleIndex),
        limitUnits: limit?.cycle_limit_units === null || limit?.cycle_limit_units === undefined
          ? null : BigInt(limit.cycle_limit_units),
        heldUnits: usage ? BigInt(usage.held_units) : 0n,
        settledUnits: usage ? BigInt(usage.settled_units) : 0n,
      };
      if (cycle.limitUnits !== null && cycle.heldUnits + cycle.settledUnits + authorizedUnits > cycle.limitUnits) {
        throw new BillingError('member_cycle_limit', 409);
      }
    }

    const holdId = randomUUID();
    const { rows: [created] } = await connection.query(`
      INSERT INTO credit_holds (
        id, account_id, app_id, user_id, enterprise_subscription_id, enterprise_cycle_index,
        request_id, status, authorized_units, pricing_snapshot, expires_at
      ) VALUES ($1, $2, $3, $4, $5, $6, $7, 'reserved', $8, $9::jsonb, statement_timestamp() + interval '15 minutes')
      RETURNING *`, [
      holdId, accountId, appId, userId,
      cycle?.subscriptionId ?? null, cycle?.cycleIndex ?? null,
      requestId, authorizedUnits.toString(), JSON.stringify(pricingSnapshot),
    ]);

    const { rows: batches } = await connection.query(`
      SELECT id, source_type AS "sourceType", expires_at AS "expiresAt", available_units AS "availableUnits"
      FROM credit_batches
      WHERE account_id = $1 AND app_id = $2 AND available_units > 0
        AND NOT frozen_for_new_holds AND expires_at > statement_timestamp()
      ORDER BY expires_at ASC,
        CASE source_type WHEN 'plan_base' THEN 0 WHEN 'credit_pack' THEN 1 ELSE 2 END ASC,
        id ASC
      FOR UPDATE`, [accountId, appId]);
    let allocation;
    try {
      allocation = allocateCreditUnits(batches, authorizedUnits);
    } catch {
      throw new BillingError('insufficient_credits', 409);
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
        randomUUID(), item.batchId, accountId, appId, (-item.units).toString(), item.units.toString(), holdId,
        `hold:${holdId}:${item.batchId}`,
      ]);
    }

    if (cycle) {
      await connection.query(`
        INSERT INTO enterprise_member_cycle_usage (
          account_id, app_id, user_id, subscription_id, cycle_index, held_units
        ) VALUES ($1, $2, $3, $4, $5, $6)
        ON CONFLICT (account_id, app_id, user_id, subscription_id, cycle_index)
        DO UPDATE SET held_units = enterprise_member_cycle_usage.held_units + EXCLUDED.held_units`, [
        accountId, appId, userId, cycle.subscriptionId, cycle.cycleIndex, authorizedUnits.toString(),
      ]);
    }
    return { hold: serializeHold(created), duplicate: false };
  });
}

async function closeHold(connection, holdId, requestedSettledUnits, mode) {
  const hold = await readHold(connection, holdId, true);
  if (!hold) throw new BillingError('credit_hold_not_found', 404);
  if (hold.status === 'settled' || hold.status === 'released') {
    return { hold: serializeHold(hold), duplicate: true, platformDifferenceUnits: '0' };
  }

  const authorizedUnits = BigInt(hold.authorized_units);
  const settledUnits = mode === 'release'
    ? 0n
    : requestedSettledUnits > authorizedUnits ? authorizedUnits : requestedSettledUnits;
  let remainingSettled = settledUnits;
  let remainingReleased = authorizedUnits - settledUnits;
  const { rows: allocations } = await connection.query(`
    SELECT * FROM credit_hold_batches WHERE hold_id = $1 ORDER BY batch_id ASC FOR UPDATE`, [holdId]);
  for (const allocation of allocations) {
    const heldUnits = BigInt(allocation.held_units);
    const settled = heldUnits < remainingSettled ? heldUnits : remainingSettled;
    remainingSettled -= settled;
    const releasable = heldUnits - settled;
    const released = releasable < remainingReleased ? releasable : remainingReleased;
    remainingReleased -= released;
    const { rows: [updatedBatch] } = await connection.query(`
      UPDATE credit_batches
      SET available_units = available_units + $1,
          held_units = held_units - $2,
          settled_units = settled_units + $3
      WHERE id = $4
      RETURNING id, held_units`, [released.toString(), (settled + released).toString(), settled.toString(), allocation.batch_id]);
    await connection.query(`
      UPDATE credit_hold_batches
      SET settled_units = $1, released_units = $2
      WHERE hold_id = $3 AND batch_id = $4`, [settled.toString(), released.toString(), holdId, allocation.batch_id]);
    if (settled + released > 0n) {
      await connection.query(`
        INSERT INTO credit_ledger_entries (
          id, batch_id, account_id, app_id, entry_type, delta_available_units,
          delta_held_units, delta_settled_units, delta_expired_units, ref_id, idempotency_key
        ) SELECT $1, $2, account_id, app_id, $3, $4, $5, $6, 0, $7, $8
        FROM credit_batches WHERE id = $2`, [
        randomUUID(), allocation.batch_id, settled > 0n ? 'settle' : 'release', released.toString(),
        (-(settled + released)).toString(), settled.toString(), holdId,
        `${settled > 0n ? 'settle' : 'release'}:${holdId}:${allocation.batch_id}`,
      ]);
    }
    if (updatedBatch && BigInt(updatedBatch.held_units) === 0n) {
      for (const hook of batchSettledHooks) {
        await hook(connection, updatedBatch.id);
      }
    }
  }

  if (hold.enterprise_subscription_id !== null && hold.enterprise_cycle_index !== null) {
    const { rows: [enterpriseUsage] } = await connection.query(`
      SELECT account_id, app_id, user_id, subscription_id, cycle_index, held_units
      FROM enterprise_member_cycle_usage
      WHERE account_id = $1 AND app_id = $2 AND user_id = $3
        AND subscription_id = $4 AND cycle_index = $5
      FOR UPDATE`, [hold.account_id, hold.app_id, hold.user_id, hold.enterprise_subscription_id, hold.enterprise_cycle_index]);
    if (enterpriseUsage) {
      await connection.query(`
        UPDATE enterprise_member_cycle_usage
        SET held_units = held_units - $1, settled_units = settled_units + $2
        WHERE account_id = $3 AND app_id = $4 AND user_id = $5
          AND subscription_id = $6 AND cycle_index = $7`, [
        authorizedUnits.toString(), settledUnits.toString(), enterpriseUsage.account_id, enterpriseUsage.app_id,
        enterpriseUsage.user_id, enterpriseUsage.subscription_id, enterpriseUsage.cycle_index,
      ]);
    }
  }

  const status = mode === 'release' ? 'released' : 'settled';
  const { rows: [updated] } = await connection.query(`
    UPDATE credit_holds
    SET status = $1, settled_units = $2, released_units = $3, settled_at = statement_timestamp()
    WHERE id = $4
    RETURNING *`, [status, settledUnits.toString(), (authorizedUnits - settledUnits).toString(), holdId]);
  return {
    hold: serializeHold(updated),
    duplicate: false,
    platformDifferenceUnits: (requestedSettledUnits > authorizedUnits ? requestedSettledUnits - authorizedUnits : 0n).toString(),
  };
}

export async function settleCredits(pool, { holdId, settledUnits: rawUnits }) {
  const settledUnits = parseAmountMinor(rawUnits);
  if (!settledUnits) throw new BillingError('invalid_settlement_units', 400);
  return transaction(pool, (connection) => closeHold(connection, holdId, settledUnits, 'settle'));
}

export async function releaseCredits(pool, holdId) {
  return transaction(pool, (connection) => closeHold(connection, holdId, 0n, 'release'));
}

export async function creditBalance(pool, { accountId, appId }) {
  const { rows: [balance] } = await pool.query(`
    SELECT COALESCE(sum(available_units), 0)::text AS available_units,
      COALESCE(sum(held_units), 0)::text AS held_units,
      COALESCE(sum(settled_units), 0)::text AS settled_units,
      COALESCE(sum(expired_units), 0)::text AS expired_units,
      COALESCE(sum(granted_units), 0)::text AS granted_units
    FROM credit_batches WHERE account_id = $1 AND app_id = $2`, [accountId, appId]);
  return {
    accountId,
    appId,
    availableUnits: balance.available_units,
    heldUnits: balance.held_units,
    settledUnits: balance.settled_units,
    expiredUnits: balance.expired_units,
    grantedUnits: balance.granted_units,
  };
}

export async function expireCreditBatches(pool, { now = new Date() } = {}) {
  return transaction(pool, async (connection) => {
    const { rows: expiredRows } = await connection.query(`
      SELECT id, account_id, app_id, available_units
      FROM credit_batches
      WHERE expires_at <= $1 AND available_units > 0
      ORDER BY expires_at ASC, id ASC
      FOR UPDATE`, [now]);
    for (const batch of expiredRows) {
      const units = BigInt(batch.available_units);
      if (units <= 0n) continue;
      await connection.query(`
        UPDATE credit_batches
        SET available_units = 0, expired_units = expired_units + $1
        WHERE id = $2`, [units.toString(), batch.id]);
      await connection.query(`
        INSERT INTO credit_ledger_entries (
          id, batch_id, account_id, app_id, entry_type, delta_available_units,
          delta_held_units, delta_settled_units, delta_expired_units, ref_id, idempotency_key
        ) VALUES ($1, $2, $3, $4, 'expire', $5, 0, 0, $6, $2::text, $7)
        ON CONFLICT (idempotency_key) DO NOTHING`, [
        randomUUID(), batch.id, batch.account_id, batch.app_id,
        (-units).toString(), units.toString(), `expire:${batch.id}`,
      ]);
    }
    return { expiredBatches: expiredRows.length };
  });
}

export async function listCreditLedger(pool, { accountId, appId = null }) {
  const { rows } = await pool.query(`
    SELECT * FROM credit_ledger_entries
    WHERE account_id = $1 AND ($2::uuid IS NULL OR app_id = $2)
    ORDER BY created_at DESC, id DESC LIMIT 200`, [accountId, appId]);
  return rows.map((row) => ({
    id: row.id,
    batchId: row.batch_id,
    accountId: row.account_id,
    appId: row.app_id,
    entryType: row.entry_type,
    deltaAvailableUnits: BigInt(row.delta_available_units).toString(),
    deltaHeldUnits: BigInt(row.delta_held_units).toString(),
    deltaSettledUnits: BigInt(row.delta_settled_units).toString(),
    deltaExpiredUnits: BigInt(row.delta_expired_units).toString(),
    refId: row.ref_id,
    idempotencyKey: row.idempotency_key,
    createdAt: row.created_at,
  }));
}

export { closeHold };
