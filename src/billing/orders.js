import { randomUUID } from 'node:crypto';
import { resolveCommerceVersion } from '../commerce/versions.js';
import { transaction } from '../database.js';
import { BillingError } from './errors.js';
import { BILLING_UNITS_PER_CREDIT, normalizeCheckoutInput } from './validation.js';

function mapSubscription(row) {
  if (!row) return null;
  return {
    id: row.id,
    accountId: row.account_id,
    appId: row.app_id,
    commerceVersionId: row.commerce_version_id,
    sourceOrderId: row.source_order_id,
    tierId: row.tier_id,
    tierRank: row.tier_rank,
    billingCycle: row.billing_cycle,
    seatCount: row.seat_count,
    status: row.status,
    autoRenew: row.auto_renew,
    authorizedRenewPriceMinor: row.authorized_renew_price_minor === null
      ? null : BigInt(row.authorized_renew_price_minor).toString(),
    periodStart: row.period_start,
    periodEnd: row.period_end,
    currentGrantCycleIndex: row.current_grant_cycle_index,
    scheduledChange: row.scheduled_change_json,
    updatedAt: row.updated_at,
  };
}

function mapOrder(row) {
  if (!row) return null;
  return {
    id: row.id,
    orderNo: row.order_no,
    accountId: row.account_id,
    buyerUserId: row.buyer_user_id,
    appId: row.app_id,
    orderType: row.order_type,
    commerceVersionId: row.commerce_version_id,
    cardTierId: row.card_tier_id,
    quantity: row.quantity,
    region: row.region,
    currency: row.currency,
    paidMinor: BigInt(row.paid_minor).toString(),
    refundedMinor: BigInt(row.refunded_minor).toString(),
    snapshot: row.snapshot_json,
    status: row.status,
    lagoInvoiceId: row.lago_invoice_id,
    lagoSyncStatus: row.lago_sync_status,
    createdAt: row.created_at,
  };
}

function jsonSafe(value) {
  return JSON.stringify(value, (_, item) => typeof item === 'bigint' ? item.toString() : item);
}

function orderNo() {
  return `UH-${Date.now().toString(36).toUpperCase()}-${randomUUID().replaceAll('-', '').slice(0, 16).toUpperCase()}`;
}

async function readPurchaseAccount(connection, accountId, userId, appId) {
  const { rows: [account] } = await connection.query(`
    SELECT a.id, a.kind, m.role, COALESCE(m.can_purchase, false) AS can_purchase,
      EXISTS (
        SELECT 1 FROM enterprise_app_seats e
        WHERE e.account_id = a.id AND e.app_id = $3 AND e.user_id = $2
      ) AS has_app_seat
    FROM accounts a
    LEFT JOIN enterprise_members m ON m.account_id = a.id AND m.user_id = $2
    WHERE a.id = $1 AND (a.kind = 'personal' AND a.owner_id = $2 OR a.kind = 'enterprise' AND m.user_id IS NOT NULL)`,
  [accountId, userId, appId]);
  if (!account) throw new BillingError('account_forbidden', 403);
  if (account.kind === 'enterprise'
    && (!account.can_purchase || (account.role === 'member' && !account.has_app_seat))) {
    throw new BillingError('purchase_forbidden', 403);
  }
  return account;
}

function cardFromVersion(version, tierId) {
  return version?.cardsSnapshot?.find((card) => card.tierId === tierId) ?? null;
}

function readPrice(card) {
  const price = card?.rule?.priceMinor;
  return Number.isSafeInteger(price) && price >= 0 ? BigInt(price) : null;
}

function cycleForCard(card) {
  return card?.rule?.billingCycle === 'yearly' ? 'yearly' : card?.rule?.billingCycle === 'monthly' ? 'monthly' : null;
}

function unitsForCredits(value, quantity) {
  if (!Number.isSafeInteger(value) || value < 0) return 0n;
  return BigInt(value) * BILLING_UNITS_PER_CREDIT * BigInt(quantity);
}

async function insertCreditBatch(connection, {
  accountId, appId, sourceType, sourceOrderId, subscriptionId, grantCycleIndex,
  units, expiresAt, suffix,
}) {
  if (units <= 0n) return;
  const batchId = randomUUID();
  await connection.query(`
    INSERT INTO credit_batches (
      id, account_id, app_id, source_type, source_order_id, subscription_id,
      grant_cycle_index, granted_units, available_units, expires_at
    ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8, $9)`, [
    batchId, accountId, appId, sourceType, sourceOrderId, subscriptionId,
    grantCycleIndex, units.toString(), expiresAt,
  ]);
  await connection.query(`
    INSERT INTO credit_ledger_entries (
      id, batch_id, account_id, app_id, entry_type, delta_available_units,
      delta_held_units, delta_settled_units, delta_expired_units, ref_id, idempotency_key
    ) VALUES ($1, $2, $3, $4, 'grant', $5, 0, 0, 0, $6, $7)`, [
    randomUUID(), batchId, accountId, appId, units.toString(), sourceOrderId,
    `grant:${sourceOrderId}:${suffix}`,
  ]);
}

async function fulfillOrder(connection, orderId) {
  const { rows: [order] } = await connection.query(
    'SELECT * FROM commerce_orders WHERE id = $1 FOR UPDATE', [orderId],
  );
  if (!order) throw new BillingError('order_not_found', 404);
  if (order.status === 'fulfilled') return mapOrder(order);
  const snapshot = order.snapshot_json;
  const card = snapshot?.card;
  const rule = card?.rule ?? {};
  let subscriptionId = null;
  let expiresAt;
  if (order.order_type === 'seat_expand') {
    const { rows: [subscription] } = await connection.query(`
      UPDATE app_subscriptions
      SET seat_count = seat_count + $1, updated_at = statement_timestamp()
      WHERE account_id = $2 AND app_id = $3 AND status = 'active'
      RETURNING *, statement_timestamp() + interval '1 month' AS default_grant_expires_at`,
    [order.quantity, order.account_id, order.app_id]);
    if (!subscription) throw new BillingError('subscription_not_found', 409);
    const expandGrant = snapshot.expandGrant ?? null;
    const baseUnits = expandGrant ? BigInt(expandGrant.baseUnits) : unitsForCredits(rule.baseCredits, order.quantity);
    const bonusUnits = expandGrant ? BigInt(expandGrant.bonusUnits) : unitsForCredits(rule.bonusCredits, order.quantity);
    const grantExpiry = expandGrant?.expiresAt ?? subscription.default_grant_expires_at;
    await insertCreditBatch(connection, {
      accountId: order.account_id, appId: order.app_id, sourceType: 'plan_base',
      sourceOrderId: order.id, subscriptionId: subscription.id,
      grantCycleIndex: subscription.current_grant_cycle_index,
      units: baseUnits, expiresAt: grantExpiry, suffix: `expand-base:${subscription.current_grant_cycle_index}`,
    });
    await insertCreditBatch(connection, {
      accountId: order.account_id, appId: order.app_id, sourceType: 'plan_bonus',
      sourceOrderId: order.id, subscriptionId: subscription.id,
      grantCycleIndex: subscription.current_grant_cycle_index,
      units: bonusUnits, expiresAt: grantExpiry, suffix: `expand-bonus:${subscription.current_grant_cycle_index}`,
    });
  } else if (order.order_type !== 'credit_pack') {
    const cycle = rule.billingCycle === 'yearly' ? 'yearly' : 'monthly';
    const { rows: [subscription] } = await connection.query(`
      INSERT INTO app_subscriptions (
        id, account_id, app_id, commerce_version_id, source_order_id, tier_id, tier_rank,
        billing_cycle, seat_count, status, auto_renew, authorized_renew_price_minor,
        period_start, period_end
      ) VALUES (
        $1, $2, $3, $4, $5, $6, $7, $8, $9, 'active', $10, $11,
        statement_timestamp(), statement_timestamp() + CASE WHEN $8 = 'yearly' THEN interval '1 year' ELSE interval '1 month' END
      ) RETURNING *, statement_timestamp() + interval '1 month' AS grant_expires_at`, [
      randomUUID(), order.account_id, order.app_id, order.commerce_version_id, order.id,
      order.card_tier_id, card.tierRank ?? 0, cycle, order.quantity,
      snapshot.autoRenew === true, BigInt(order.paid_minor).toString(),
    ]);
    subscriptionId = subscription.id;
    expiresAt = subscription.grant_expires_at;
    await insertCreditBatch(connection, {
      accountId: order.account_id, appId: order.app_id, sourceType: 'plan_base',
      sourceOrderId: order.id, subscriptionId, grantCycleIndex: 0,
      units: unitsForCredits(rule.baseCredits, order.quantity), expiresAt, suffix: 'base:0',
    });
    await insertCreditBatch(connection, {
      accountId: order.account_id, appId: order.app_id, sourceType: 'plan_bonus',
      sourceOrderId: order.id, subscriptionId, grantCycleIndex: 0,
      units: unitsForCredits(rule.bonusCredits, order.quantity), expiresAt, suffix: 'bonus:0',
    });
  } else {
    const validDays = Number.isSafeInteger(rule.validDays) && rule.validDays > 0 ? rule.validDays : 1;
    const { rows: [expiry] } = await connection.query(
      `SELECT statement_timestamp() + ($1::integer * interval '1 day') AS expires_at`, [validDays],
    );
    expiresAt = expiry.expires_at;
    await insertCreditBatch(connection, {
      accountId: order.account_id, appId: order.app_id, sourceType: 'credit_pack',
      sourceOrderId: order.id, subscriptionId: null, grantCycleIndex: null,
      units: unitsForCredits(rule.packCredits, order.quantity), expiresAt, suffix: 'pack:0',
    });
  }

  const { rows: [fulfilled] } = await connection.query(`
    UPDATE commerce_orders SET status = 'fulfilled' WHERE id = $1 RETURNING *`, [order.id]);
  return mapOrder(fulfilled);
}

export async function createCheckout(pool, {
  accountId, userId, region, currency, input,
}) {
  const normalized = normalizeCheckoutInput(input);
  if (!normalized) throw new BillingError('invalid_checkout', 400);
  return transaction(pool, async (connection) => {
    const account = await readPurchaseAccount(connection, accountId, userId, normalized.appId);
    if (account.kind === 'personal' && normalized.quantity !== 1) {
      throw new BillingError('invalid_quantity', 400);
    }
    await connection.query('SELECT id FROM accounts WHERE id = $1 FOR UPDATE', [accountId]);
    const version = normalized.versionId
      ? await (async () => {
        const { rows: [selected] } = await connection.query(`
          SELECT id, version_code AS "versionCode", app_id AS "appId", account_kind AS "accountKind",
            region, currency, effective_at AS "effectiveAt", cards_snapshot AS "cardsSnapshot"
          FROM commerce_versions WHERE id = $1 AND app_id = $2 AND region = $3`, [normalized.versionId, normalized.appId, region]);
        return selected;
      })()
      : await resolveCommerceVersion(connection, {
        appId: normalized.appId, accountKind: account.kind, region, at: new Date(),
      });
    if (!version || version.accountKind !== account.kind || version.currency !== currency) {
      throw new BillingError('commerce_version_not_found', 404);
    }
    const card = cardFromVersion(version, normalized.tierId);
    if (!card) throw new BillingError('product_not_found', 404);
    const price = readPrice(card);
    if (price === null) throw new BillingError('invalid_product_price', 409);
    const productType = card.productType;
    const expectedOrderType = productType === 'credit_pack' ? 'credit_pack' : normalized.orderType;
    if (productType === 'credit_pack' && normalized.orderType !== 'subscribe' && normalized.orderType !== 'credit_pack') {
      throw new BillingError('invalid_order_type', 400);
    }
    if (productType === 'membership' && normalized.orderType === 'credit_pack') {
      throw new BillingError('invalid_order_type', 400);
    }
    const orderType = expectedOrderType;
    if (orderType === 'renew' || orderType === 'upgrade') {
      throw new BillingError('order_type_requires_stage2c', 409);
    }
    const active = await connection.query(`
      SELECT * FROM app_subscriptions
      WHERE account_id = $1 AND app_id = $2 AND status = 'active'
      FOR UPDATE`, [accountId, normalized.appId]);
    const activeSubscription = active.rows[0] ?? null;
    if (orderType === 'subscribe' && activeSubscription) throw new BillingError('subscription_active', 409);
    if (orderType === 'seat_expand' && (!activeSubscription || account.kind !== 'enterprise')) {
      throw new BillingError('subscription_not_found', 409);
    }
    if (productType === 'credit_pack' && card.rule?.packEligibility === 'active_member' && !activeSubscription) {
      throw new BillingError('membership_required', 403);
    }
    const total = price * BigInt(normalized.quantity);
    const { rows: [wallet] } = await connection.query(
      'SELECT * FROM wallet_accounts WHERE account_id = $1 FOR UPDATE', [accountId],
    );
    if (!wallet || wallet.region !== region || wallet.currency !== currency) throw new BillingError('wallet_not_found', 404);
    const available = BigInt(wallet.available_minor);
    if (BigInt(wallet.arrears_minor) > 0n || wallet.frozen_reason || available < total) {
      throw new BillingError('insufficient_funds', 402, {
        amountMinor: total.toString(),
        availableMinor: available.toString(),
        pendingCheckout: {
          accountId, appId: normalized.appId, versionId: version.id, tierId: normalized.tierId,
          quantity: normalized.quantity, orderType, autoRenew: normalized.autoRenew,
        },
      });
    }

    const id = randomUUID();
    const snapshot = {
      appId: normalized.appId,
      accountId,
      accountKind: account.kind,
      region,
      currency,
      versionId: version.id,
      versionCode: version.versionCode,
      card,
      quantity: normalized.quantity,
      unitPriceMinor: price.toString(),
      autoRenew: normalized.autoRenew,
    };
    const { rows: [created] } = await connection.query(`
      INSERT INTO commerce_orders (
        id, order_no, account_id, buyer_user_id, app_id, order_type, commerce_version_id,
        card_tier_id, quantity, region, currency, paid_minor, snapshot_json, status
      ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb, 'confirmed')
      RETURNING *`, [
      id, orderNo(), accountId, userId, normalized.appId, orderType, version.id,
      normalized.tierId, normalized.quantity, region, currency, total.toString(), jsonSafe(snapshot),
    ]);
    const nextAvailable = available - total;
    await connection.query(`
      UPDATE wallet_accounts SET available_minor = $1, updated_at = statement_timestamp()
      WHERE account_id = $2`, [nextAvailable.toString(), accountId]);
    await connection.query(`
      INSERT INTO cash_ledger_entries (
        id, account_id, region, currency, entry_type, delta_minor, balance_after_minor,
        arrears_after_minor, ref_type, ref_id, idempotency_key
      ) VALUES ($1, $2, $3, $4, 'purchase', $5, $6, $7, 'commerce_order', $8, $9)`, [
      randomUUID(), accountId, region, currency, (-total).toString(), nextAvailable.toString(),
      wallet.arrears_minor, id, `purchase:${id}`,
    ]);
    await connection.query(`
      INSERT INTO billing_outbox (id, dedupe_key, topic, payload_json)
      VALUES ($1, $2, 'grant_entitlement', $3::jsonb)`, [
      randomUUID(), `grant:${id}:cycle:0`, JSON.stringify({ orderId: id }),
    ]);
    return { order: mapOrder(created), wallet: { availableMinor: nextAvailable.toString(), arrearsMinor: wallet.arrears_minor } };
  });
}

export async function readOrder(pool, orderId, accountId = null) {
  const { rows: [order] } = await pool.query(`
    SELECT * FROM commerce_orders WHERE id = $1 AND ($2::uuid IS NULL OR account_id = $2)`, [orderId, accountId]);
  return mapOrder(order);
}

export async function listOrders(pool, accountId) {
  const { rows } = await pool.query(
    'SELECT * FROM commerce_orders WHERE account_id = $1 ORDER BY created_at DESC LIMIT 100', [accountId],
  );
  return rows.map(mapOrder);
}

export async function listSubscriptions(pool, accountId) {
  const { rows } = await pool.query(`
    SELECT * FROM app_subscriptions WHERE account_id = $1
    ORDER BY CASE WHEN status = 'active' THEN 0 ELSE 1 END, period_end DESC LIMIT 100`, [accountId]);
  return rows.map(mapSubscription);
}

export async function processPendingOutbox(pool, { limit = 20 } = {}) {
  const max = Number.isSafeInteger(limit) && limit > 0 ? Math.min(limit, 100) : 20;
  let processed = 0;
  const failed = [];
  while (processed + failed.length < max) {
    let row;
    try {
      row = await transaction(pool, async (connection) => {
        const { rows: [pending] } = await connection.query(`
          SELECT * FROM billing_outbox
          WHERE status = 'pending' AND next_retry_at <= statement_timestamp()
          ORDER BY created_at, id LIMIT 1 FOR UPDATE SKIP LOCKED`);
        if (!pending) return null;
        if (pending.topic === 'grant_entitlement') await fulfillOrder(connection, pending.payload_json.orderId);
        await connection.query(`
          UPDATE billing_outbox SET status = 'done', attempts = attempts + 1, last_error = NULL
          WHERE id = $1`, [pending.id]);
        return pending.id;
      });
    } catch (error) {
      const message = error instanceof Error ? error.message.slice(0, 500) : 'outbox_failed';
      try {
        await pool.query(`
          UPDATE billing_outbox
          SET attempts = attempts + 1,
              status = CASE WHEN attempts + 1 >= 5 THEN 'dead' ELSE 'pending' END,
              next_retry_at = statement_timestamp() + (LEAST(attempts + 1, 5) * interval '1 minute'),
              last_error = $2
          WHERE id = (
            SELECT id FROM billing_outbox WHERE status = 'pending' ORDER BY created_at, id LIMIT 1
          )`, [null, message]);
      } catch {
        // The worker will retry the durable row on its next invocation.
      }
      failed.push(message);
      continue;
    }
    if (!row) break;
    processed++;
  }
  return { processed, failed };
}

export { insertCreditBatch, mapOrder, mapSubscription, unitsForCredits };
