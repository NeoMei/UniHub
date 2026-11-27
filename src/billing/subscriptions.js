import { randomUUID } from 'node:crypto';
import { resolveCommerceVersion } from '../commerce/versions.js';
import { isUuid, transaction } from '../database.js';
import { BillingError } from './errors.js';
import { insertCreditBatch, mapOrder, mapSubscription, unitsForCredits } from './orders.js';

function jsonSafe(value) {
  return JSON.stringify(value, (_, item) => typeof item === 'bigint' ? item.toString() : item);
}

function addMonthsUtc(date, months) {
  const next = new Date(date.getTime());
  next.setUTCMonth(next.getUTCMonth() + months);
  return next;
}

function cycleBounds(subscription, now = new Date()) {
  const start = new Date(subscription.period_start);
  const end = new Date(subscription.period_end);
  const totalMonths = subscription.billing_cycle === 'yearly' ? 12 : 1;
  const idx = Number(subscription.current_grant_cycle_index);
  const curStart = addMonthsUtc(start, idx);
  const rawNext = addMonthsUtc(start, idx + 1);
  const curEnd = rawNext.getTime() < end.getTime() ? rawNext : end;
  const tCur = BigInt(Math.max(1, curEnd.getTime() - curStart.getTime()));
  const rawRem = Math.max(0, curEnd.getTime() - now.getTime());
  const tRem = BigInt(rawRem) > tCur ? tCur : BigInt(rawRem);
  const mFuture = BigInt(Math.max(0, totalMonths - 1 - idx));
  return { curEnd, tCur, tRem, mFuture };
}

export function computeSeatExpansionProration({
  deltaSeats,
  unitMonthMinor,
  baseCreditsPerSeat,
  bonusCreditsPerSeat,
  tCur,
  tRem,
  mFuture,
}) {
  const ds = BigInt(deltaSeats);
  const deltaMinor = (ds * unitMonthMinor * tRem) / tCur + ds * unitMonthMinor * mFuture;
  const baseUnits = (ds * BigInt(baseCreditsPerSeat) * 1_000_000n * tRem) / tCur;
  const bonusUnits = (ds * BigInt(bonusCreditsPerSeat) * 1_000_000n * tRem) / tCur;
  return { deltaMinor, baseUnits, bonusUnits };
}

async function lockEnterpriseManagerOrPurchaser(connection, accountId, userId, appId) {
  const { rows: [account] } = await connection.query(`
    SELECT a.id, a.kind, m.role, COALESCE(m.can_purchase, false) AS can_purchase,
      EXISTS (
        SELECT 1 FROM enterprise_app_seats e
        WHERE e.account_id = a.id AND e.app_id = $3 AND e.user_id = $2
      ) AS has_app_seat
    FROM accounts a
    LEFT JOIN enterprise_members m ON m.account_id = a.id AND m.user_id = $2
    WHERE a.id = $1 AND a.kind = 'enterprise'`, [accountId, userId, appId]);
  if (!account || (!account.can_purchase && account.role !== 'owner')) {
    throw new BillingError('purchase_forbidden', 403);
  }
  if (account.role === 'member' && !account.has_app_seat) {
    throw new BillingError('purchase_forbidden', 403);
  }
  return account;
}

export async function quoteSeatExpansion(pool, {
  accountId, subscriptionId, deltaSeats, now = new Date(),
}) {
  if (!isUuid(accountId) || !isUuid(subscriptionId) || !Number.isSafeInteger(deltaSeats) || deltaSeats < 1) {
    throw new BillingError('invalid_seat_expansion', 400);
  }
  const { rows: [sub] } = await pool.query(
    "SELECT * FROM app_subscriptions WHERE id = $1 AND account_id = $2 AND status = 'active'",
    [subscriptionId, accountId],
  );
  if (!sub) throw new BillingError('subscription_not_found', 404);
  const { rows: [order] } = await pool.query('SELECT * FROM commerce_orders WHERE id = $1', [sub.source_order_id]);
  const rule = order?.snapshot_json?.card?.rule;
  if (!rule) throw new BillingError('subscription_snapshot_missing', 409);

  const unitPrice = BigInt(rule.priceMinor);
  const unitMonthMinor = sub.billing_cycle === 'yearly' ? unitPrice / 12n : unitPrice;
  const { curEnd, tCur, tRem, mFuture } = cycleBounds(sub, now);
  const calc = computeSeatExpansionProration({
    deltaSeats,
    unitMonthMinor,
    baseCreditsPerSeat: rule.baseCredits,
    bonusCreditsPerSeat: rule.bonusCredits ?? 0,
    tCur,
    tRem,
    mFuture,
  });

  return {
    subscriptionId: sub.id,
    currentSeatCount: sub.seat_count,
    deltaSeats,
    newSeatCount: sub.seat_count + deltaSeats,
    deltaMinor: calc.deltaMinor.toString(),
    baseGrantUnits: calc.baseUnits.toString(),
    bonusGrantUnits: calc.bonusUnits.toString(),
    cycleExpiresAt: curEnd.toISOString(),
  };
}

export async function executeSeatExpansion(pool, {
  accountId, userId, subscriptionId, deltaSeats, region, currency, now = new Date(),
}) {
  if (!isUuid(accountId) || !isUuid(userId) || !isUuid(subscriptionId)
    || !Number.isSafeInteger(deltaSeats) || deltaSeats < 1) {
    throw new BillingError('invalid_seat_expansion', 400);
  }
  return transaction(pool, async (connection) => {
    const { rows: [sub] } = await connection.query(
      "SELECT * FROM app_subscriptions WHERE id = $1 AND account_id = $2 AND status = 'active' FOR UPDATE",
      [subscriptionId, accountId],
    );
    if (!sub) throw new BillingError('subscription_not_found', 404);
    await lockEnterpriseManagerOrPurchaser(connection, accountId, userId, sub.app_id);

    const { rows: [sourceOrder] } = await connection.query(
      'SELECT * FROM commerce_orders WHERE id = $1', [sub.source_order_id],
    );
    const card = sourceOrder?.snapshot_json?.card;
    const rule = card?.rule;
    if (!rule) throw new BillingError('subscription_snapshot_missing', 409);

    const unitPrice = BigInt(rule.priceMinor);
    const unitMonthMinor = sub.billing_cycle === 'yearly' ? unitPrice / 12n : unitPrice;
    const { curEnd, tCur, tRem, mFuture } = cycleBounds(sub, now);
    const calc = computeSeatExpansionProration({
      deltaSeats,
      unitMonthMinor,
      baseCreditsPerSeat: rule.baseCredits,
      bonusCreditsPerSeat: rule.bonusCredits ?? 0,
      tCur,
      tRem,
      mFuture,
    });

    const { rows: [wallet] } = await connection.query(
      'SELECT * FROM wallet_accounts WHERE account_id = $1 FOR UPDATE', [accountId],
    );
    if (!wallet || wallet.region !== region || wallet.currency !== currency) {
      throw new BillingError('wallet_not_found', 404);
    }
    const available = BigInt(wallet.available_minor);
    if (BigInt(wallet.arrears_minor) > 0n || wallet.frozen_reason || available < calc.deltaMinor) {
      throw new BillingError('insufficient_funds', 402, {
        amountMinor: calc.deltaMinor.toString(),
        availableMinor: available.toString(),
        pendingCheckout: {
          accountId, appId: sub.app_id, versionId: sub.commerce_version_id, tierId: sub.tier_id,
          quantity: deltaSeats, orderType: 'seat_expand',
        },
      });
    }

    const orderId = randomUUID();
    const orderNo = `UH-SEAT-${Date.now().toString(36).toUpperCase()}-${randomUUID().replaceAll('-', '').slice(0, 10).toUpperCase()}`;
    const snapshot = {
      appId: sub.app_id,
      accountId,
      accountKind: 'enterprise',
      region,
      currency,
      versionId: sub.commerce_version_id,
      card,
      quantity: deltaSeats,
      unitMonthMinor: unitMonthMinor.toString(),
      expandGrant: {
        baseUnits: calc.baseUnits.toString(),
        bonusUnits: calc.bonusUnits.toString(),
        expiresAt: curEnd.toISOString(),
      },
    };
    const { rows: [createdOrder] } = await connection.query(`
      INSERT INTO commerce_orders (
        id, order_no, account_id, buyer_user_id, app_id, order_type, commerce_version_id,
        card_tier_id, quantity, region, currency, paid_minor, snapshot_json, status
      ) VALUES ($1, $2, $3, $4, $5, 'seat_expand', $6, $7, $8, $9, $10, $11, $12::jsonb, 'fulfilled')
      RETURNING *`, [
      orderId, orderNo, accountId, userId, sub.app_id, sub.commerce_version_id,
      sub.tier_id, deltaSeats, region, currency, calc.deltaMinor.toString(), jsonSafe(snapshot),
    ]);

    const nextAvailable = available - calc.deltaMinor;
    await connection.query(
      'UPDATE wallet_accounts SET available_minor = $1, updated_at = statement_timestamp() WHERE account_id = $2',
      [nextAvailable.toString(), accountId],
    );
    await connection.query(`
      INSERT INTO cash_ledger_entries (
        id, account_id, region, currency, entry_type, delta_minor, balance_after_minor,
        arrears_after_minor, ref_type, ref_id, idempotency_key
      ) VALUES ($1, $2, $3, $4, 'purchase', $5, $6, $7, 'commerce_order', $8, $9)`, [
      randomUUID(), accountId, region, currency, (-calc.deltaMinor).toString(), nextAvailable.toString(),
      wallet.arrears_minor, orderId, `purchase:${orderId}`,
    ]);

    const { rows: [updatedSub] } = await connection.query(`
      UPDATE app_subscriptions SET seat_count = seat_count + $1, updated_at = statement_timestamp()
      WHERE id = $2 RETURNING *`, [deltaSeats, sub.id]);

    await insertCreditBatch(connection, {
      accountId, appId: sub.app_id, sourceType: 'plan_base', sourceOrderId: orderId,
      subscriptionId: sub.id, grantCycleIndex: sub.current_grant_cycle_index,
      units: calc.baseUnits, expiresAt: curEnd, suffix: `expand-base:${orderId}`,
    });
    await insertCreditBatch(connection, {
      accountId, appId: sub.app_id, sourceType: 'plan_bonus', sourceOrderId: orderId,
      subscriptionId: sub.id, grantCycleIndex: sub.current_grant_cycle_index,
      units: calc.bonusUnits, expiresAt: curEnd, suffix: `expand-bonus:${orderId}`,
    });

    return {
      order: mapOrder(createdOrder),
      subscription: mapSubscription(updatedSub),
      grantedBaseUnits: calc.baseUnits.toString(),
      grantedBonusUnits: calc.bonusUnits.toString(),
      wallet: { availableMinor: nextAvailable.toString(), arrearsMinor: wallet.arrears_minor },
    };
  });
}

export async function scheduleSubscriptionChange(pool, {
  accountId, userId, subscriptionId, targetTierId, targetSeatCount = null, region,
}) {
  if (!isUuid(accountId) || !isUuid(userId) || !isUuid(subscriptionId)) {
    throw new BillingError('invalid_scheduled_change', 400);
  }
  return transaction(pool, async (connection) => {
    const { rows: [sub] } = await connection.query(
      "SELECT * FROM app_subscriptions WHERE id = $1 AND account_id = $2 AND status = 'active' FOR UPDATE",
      [subscriptionId, accountId],
    );
    if (!sub) throw new BillingError('subscription_not_found', 404);
    const { rows: [account] } = await connection.query('SELECT kind FROM accounts WHERE id = $1', [accountId]);
    if (account.kind === 'enterprise') await lockEnterpriseManagerOrPurchaser(connection, accountId, userId, sub.app_id);

    const version = await resolveCommerceVersion(connection, {
      appId: sub.app_id, accountKind: account.kind, region, at: new Date(),
    });
    const targetCard = version?.cardsSnapshot?.find((c) => c.tierId === targetTierId);
    if (!targetCard || targetCard.productType !== 'membership') {
      throw new BillingError('product_not_found', 404);
    }
    const nextSeatCount = account.kind === 'enterprise'
      ? Number(targetSeatCount ?? sub.seat_count)
      : 1;
    if (!Number.isSafeInteger(nextSeatCount) || nextSeatCount < 1) {
      throw new BillingError('invalid_seat_count', 400);
    }

    const change = {
      targetTierId: targetCard.tierId,
      targetCycle: targetCard.rule.billingCycle,
      targetSeatCount: nextSeatCount,
      effectiveAt: new Date(sub.period_end).toISOString(),
    };
    const { rows: [updated] } = await connection.query(`
      UPDATE app_subscriptions SET scheduled_change_json = $1::jsonb, updated_at = statement_timestamp()
      WHERE id = $2 RETURNING *`, [JSON.stringify(change), sub.id]);
    return { subscription: mapSubscription(updated) };
  });
}

export async function cancelScheduledChange(pool, { accountId, userId, subscriptionId }) {
  if (!isUuid(accountId) || !isUuid(subscriptionId)) {
    throw new BillingError('invalid_scheduled_change', 400);
  }
  return transaction(pool, async (connection) => {
    const { rows: [sub] } = await connection.query(
      "SELECT * FROM app_subscriptions WHERE id = $1 AND account_id = $2 AND status = 'active' FOR UPDATE",
      [subscriptionId, accountId],
    );
    if (!sub) throw new BillingError('subscription_not_found', 404);
    const { rows: [account] } = await connection.query('SELECT kind FROM accounts WHERE id = $1', [accountId]);
    if (account.kind === 'enterprise') await lockEnterpriseManagerOrPurchaser(connection, accountId, userId, sub.app_id);

    const { rows: [updated] } = await connection.query(`
      UPDATE app_subscriptions SET scheduled_change_json = NULL, updated_at = statement_timestamp()
      WHERE id = $1 RETURNING *`, [sub.id]);
    return { subscription: mapSubscription(updated) };
  });
}

export async function setAutoRenew(pool, {
  accountId, userId, subscriptionId, autoRenew, authorizedPriceMinor = null,
}) {
  if (!isUuid(accountId) || !isUuid(subscriptionId) || typeof autoRenew !== 'boolean') {
    throw new BillingError('invalid_auto_renew', 400);
  }
  return transaction(pool, async (connection) => {
    const { rows: [sub] } = await connection.query(
      "SELECT * FROM app_subscriptions WHERE id = $1 AND account_id = $2 AND status = 'active' FOR UPDATE",
      [subscriptionId, accountId],
    );
    if (!sub) throw new BillingError('subscription_not_found', 404);
    const { rows: [account] } = await connection.query('SELECT kind FROM accounts WHERE id = $1', [accountId]);
    if (account.kind === 'enterprise') await lockEnterpriseManagerOrPurchaser(connection, accountId, userId, sub.app_id);

    const nextAuthPrice = authorizedPriceMinor !== null
      ? String(authorizedPriceMinor)
      : sub.authorized_renew_price_minor;
    const { rows: [updated] } = await connection.query(`
      UPDATE app_subscriptions
      SET auto_renew = $1, authorized_renew_price_minor = $2, updated_at = statement_timestamp()
      WHERE id = $3 RETURNING *`, [autoRenew, nextAuthPrice, sub.id]);
    return { subscription: mapSubscription(updated) };
  });
}

export async function processRenewalCycle(pool, { now = new Date(), region, currency }) {
  const noticesSent = [];
  const monthlyGrants = [];
  const renewed = [];
  const haltedPriceIncrease = [];
  const expiredGracefully = [];

  await transaction(pool, async (connection) => {
    // 1. Pre-expiry 3-day notice for auto-renew subscriptions
    const noticeHorizon = new Date(now.getTime() + 3 * 86_400_000);
    const { rows: upcoming } = await connection.query(`
      SELECT id, account_id, app_id, period_end
      FROM app_subscriptions
      WHERE status = 'active' AND auto_renew = true AND period_end <= $1 AND period_end > $2
      FOR UPDATE`, [noticeHorizon, now]);
    for (const sub of upcoming) {
      const dedupeKey = `renewal_notice:${sub.id}:${new Date(sub.period_end).toISOString()}`;
      const { rowCount } = await connection.query(`
        INSERT INTO billing_outbox (id, dedupe_key, topic, payload_json)
        VALUES ($1, $2, 'renewal_notice', $3::jsonb)
        ON CONFLICT (dedupe_key) DO NOTHING`, [
        randomUUID(), dedupeKey, JSON.stringify({
          subscriptionId: sub.id, accountId: sub.account_id, appId: sub.app_id,
          reason: 'upcoming_auto_renew_3d', periodEnd: sub.period_end,
        }),
      ]);
      if (rowCount === 1) noticesSent.push(sub.id);
    }

    // 2. Grant next monthly cycles on active yearly subscriptions
    const { rows: yearlySubs } = await connection.query(`
      SELECT * FROM app_subscriptions
      WHERE status = 'active' AND billing_cycle = 'yearly' AND current_grant_cycle_index < 11
      FOR UPDATE`);
    for (const sub of yearlySubs) {
      const nextIdx = Number(sub.current_grant_cycle_index) + 1;
      const nextDue = addMonthsUtc(new Date(sub.period_start), nextIdx);
      if (now < nextDue) continue;
      const { rows: [order] } = await connection.query('SELECT * FROM commerce_orders WHERE id = $1', [sub.source_order_id]);
      const rule = order?.snapshot_json?.card?.rule;
      if (!rule) continue;
      const cycleEnd = addMonthsUtc(new Date(sub.period_start), nextIdx + 1);
      const expiresAt = cycleEnd < new Date(sub.period_end) ? cycleEnd : new Date(sub.period_end);

      await connection.query(
        'UPDATE app_subscriptions SET current_grant_cycle_index = $1, updated_at = statement_timestamp() WHERE id = $2',
        [nextIdx, sub.id],
      );
      await insertCreditBatch(connection, {
        accountId: sub.account_id, appId: sub.app_id, sourceType: 'plan_base', sourceOrderId: order.id,
        subscriptionId: sub.id, grantCycleIndex: nextIdx,
        units: unitsForCredits(rule.baseCredits, sub.seat_count), expiresAt, suffix: `base:${nextIdx}`,
      });
      await insertCreditBatch(connection, {
        accountId: sub.account_id, appId: sub.app_id, sourceType: 'plan_bonus', sourceOrderId: order.id,
        subscriptionId: sub.id, grantCycleIndex: nextIdx,
        units: unitsForCredits(rule.bonusCredits ?? 0, sub.seat_count), expiresAt, suffix: `bonus:${nextIdx}`,
      });
      monthlyGrants.push({ subscriptionId: sub.id, cycleIndex: nextIdx });
    }

    // 3. Process due subscriptions at period_end <= now
    const { rows: dueSubs } = await connection.query(`
      SELECT s.*, a.kind AS account_kind, a.owner_id
      FROM app_subscriptions s
      JOIN accounts a ON a.id = s.account_id
      WHERE s.status = 'active' AND s.period_end <= $1
      FOR UPDATE`, [now]);

    for (const sub of dueSubs) {
      if (!sub.auto_renew && !sub.scheduled_change_json) {
        await connection.query(
          "UPDATE app_subscriptions SET status = 'expired', updated_at = statement_timestamp() WHERE id = $1",
          [sub.id],
        );
        expiredGracefully.push({ subscriptionId: sub.id, reason: 'auto_renew_off' });
        continue;
      }

      const scheduled = sub.scheduled_change_json;
      const targetTierId = scheduled?.targetTierId ?? sub.tier_id;
      const targetQuantity = scheduled?.targetSeatCount ?? sub.seat_count;
      const version = await resolveCommerceVersion(connection, {
        appId: sub.app_id, accountKind: sub.account_kind, region, at: now,
      });
      const targetCard = version?.cardsSnapshot?.find((c) => c.tierId === targetTierId) ?? null;
      if (!targetCard || targetCard.productType !== 'membership') {
        await connection.query(
          "UPDATE app_subscriptions SET status = 'expired', auto_renew = false, updated_at = statement_timestamp() WHERE id = $1",
          [sub.id],
        );
        expiredGracefully.push({ subscriptionId: sub.id, reason: 'card_unavailable' });
        continue;
      }

      const unitPrice = BigInt(targetCard.rule.priceMinor);
      const totalPrice = unitPrice * BigInt(targetQuantity);
      const authCap = sub.authorized_renew_price_minor === null ? null : BigInt(sub.authorized_renew_price_minor);

      // Price increase guard checks per-unit price against authorized unit price unless user scheduled the change
      if (!scheduled && sub.auto_renew && authCap !== null && unitPrice > authCap) {
        await connection.query(`
          UPDATE app_subscriptions
          SET auto_renew = false, status = 'expired', updated_at = statement_timestamp()
          WHERE id = $1`, [sub.id]);
        await connection.query(`
          INSERT INTO billing_outbox (id, dedupe_key, topic, payload_json)
          VALUES ($1, $2, 'renewal_notice', $3::jsonb)
          ON CONFLICT (dedupe_key) DO NOTHING`, [
          randomUUID(),
          `reauth:${sub.id}:${new Date(sub.period_end).toISOString()}`,
          JSON.stringify({
            subscriptionId: sub.id, accountId: sub.account_id, appId: sub.app_id,
            reason: 'price_increased_reauth_required',
            authorizedPriceMinor: authCap.toString(), currentPriceMinor: totalPrice.toString(),
          }),
        ]);
        haltedPriceIncrease.push({ subscriptionId: sub.id, currentPriceMinor: totalPrice.toString() });
        continue;
      }

      const { rows: [wallet] } = await connection.query(
        'SELECT * FROM wallet_accounts WHERE account_id = $1 FOR UPDATE', [sub.account_id],
      );
      const available = BigInt(wallet.available_minor);
      if (BigInt(wallet.arrears_minor) > 0n || wallet.frozen_reason || available < totalPrice) {
        await connection.query(
          "UPDATE app_subscriptions SET status = 'expired', updated_at = statement_timestamp() WHERE id = $1",
          [sub.id],
        );
        expiredGracefully.push({ subscriptionId: sub.id, reason: 'insufficient_funds' });
        continue;
      }

      const newOrderId = randomUUID();
      const newSubId = randomUUID();
      const orderNo = `UH-RNW-${Date.now().toString(36).toUpperCase()}-${randomUUID().replaceAll('-', '').slice(0, 10).toUpperCase()}`;
      const snapshot = {
        appId: sub.app_id, accountId: sub.account_id, accountKind: sub.account_kind,
        region, currency, versionId: version.id, versionCode: version.versionCode,
        card: targetCard, quantity: targetQuantity, unitPriceMinor: unitPrice.toString(),
        autoRenew: Boolean(sub.auto_renew),
      };
      await connection.query(`
        INSERT INTO commerce_orders (
          id, order_no, account_id, buyer_user_id, app_id, order_type, commerce_version_id,
          card_tier_id, quantity, region, currency, paid_minor, snapshot_json, status
        ) VALUES ($1, $2, $3, $4, $5, 'renew', $6, $7, $8, $9, $10, $11, $12::jsonb, 'fulfilled')`, [
        newOrderId, orderNo, sub.account_id, sub.owner_id, sub.app_id, version.id,
        targetCard.tierId, targetQuantity, region, currency, totalPrice.toString(), jsonSafe(snapshot),
      ]);

      const nextAvailable = available - totalPrice;
      await connection.query(
        'UPDATE wallet_accounts SET available_minor = $1, updated_at = statement_timestamp() WHERE account_id = $2',
        [nextAvailable.toString(), sub.account_id],
      );
      await connection.query(`
        INSERT INTO cash_ledger_entries (
          id, account_id, region, currency, entry_type, delta_minor, balance_after_minor,
          arrears_after_minor, ref_type, ref_id, idempotency_key
        ) VALUES ($1, $2, $3, $4, 'purchase', $5, $6, $7, 'commerce_order', $8, $9)`, [
        randomUUID(), sub.account_id, region, currency, (-totalPrice).toString(), nextAvailable.toString(),
        wallet.arrears_minor, newOrderId, `purchase:${newOrderId}`,
      ]);

      await connection.query(
        "UPDATE app_subscriptions SET status = 'superseded', updated_at = statement_timestamp() WHERE id = $1",
        [sub.id],
      );
      const nextCycle = targetCard.rule.billingCycle === 'yearly' ? 'yearly' : 'monthly';
      const newPeriodStart = new Date(sub.period_end);
      const newPeriodEnd = addMonthsUtc(newPeriodStart, nextCycle === 'yearly' ? 12 : 1);
      const grantExpiresAt = addMonthsUtc(newPeriodStart, 1);
      await connection.query(`
        INSERT INTO app_subscriptions (
          id, account_id, app_id, commerce_version_id, source_order_id, tier_id, tier_rank,
          billing_cycle, seat_count, status, auto_renew, authorized_renew_price_minor,
          period_start, period_end
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'active', $10, $11, $12, $13)`, [
        newSubId, sub.account_id, sub.app_id, version.id, newOrderId, targetCard.tierId,
        targetCard.tierRank ?? 0, nextCycle, targetQuantity, Boolean(sub.auto_renew),
        unitPrice.toString(), newPeriodStart, newPeriodEnd,
      ]);

      await insertCreditBatch(connection, {
        accountId: sub.account_id, appId: sub.app_id, sourceType: 'plan_base', sourceOrderId: newOrderId,
        subscriptionId: newSubId, grantCycleIndex: 0,
        units: unitsForCredits(targetCard.rule.baseCredits, targetQuantity),
        expiresAt: grantExpiresAt, suffix: 'base:0',
      });
      await insertCreditBatch(connection, {
        accountId: sub.account_id, appId: sub.app_id, sourceType: 'plan_bonus', sourceOrderId: newOrderId,
        subscriptionId: newSubId, grantCycleIndex: 0,
        units: unitsForCredits(targetCard.rule.bonusCredits ?? 0, targetQuantity),
        expiresAt: grantExpiresAt, suffix: 'bonus:0',
      });
      renewed.push({ oldSubscriptionId: sub.id, newSubscriptionId: newSubId, orderId: newOrderId });
    }
  });

  return { noticesSent, monthlyGrants, renewed, haltedPriceIncrease, expiredGracefully };
}
