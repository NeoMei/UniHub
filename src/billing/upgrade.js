import { randomUUID } from 'node:crypto';
import { resolveCommerceVersion } from '../commerce/versions.js';
import { isUuid, transaction } from '../database.js';
import { registerBatchSettledHook } from './credits.js';
import { BillingError } from './errors.js';
import {
  clearPendingCheckout,
  enqueueLagoInvoiceSync,
  insertCreditBatch,
  mapOrder,
  mapSubscription,
  unitsForCredits,
} from './orders.js';

function jsonSafe(value) {
  return JSON.stringify(value, (_, item) => typeof item === 'bigint' ? item.toString() : item);
}

function orderNo() {
  return `UH-UPG-${Date.now().toString(36).toUpperCase()}-${randomUUID().replaceAll('-', '').slice(0, 12).toUpperCase()}`;
}

export function allocateMonthlyMinor(paidMinor, months) {
  if (typeof paidMinor !== 'bigint' || paidMinor < 0n || !Number.isInteger(months) || months < 1) {
    throw new TypeError('invalid monthly allocation inputs');
  }
  const m = BigInt(months);
  const base = paidMinor / m;
  const rem = paidMinor % m;
  return Array.from({ length: months }, (_, idx) => base + (BigInt(idx) < rem ? 1n : 0n));
}

export function computeUpgradeProration({
  paidMinor,
  refundedMinor = 0n,
  months,
  currentCycleIndex = 0,
  baseGrantedUnits,
  baseConsumedUnits,
}) {
  const allocations = allocateMonthlyMinor(paidMinor, months);
  if (currentCycleIndex < 0 || currentCycleIndex >= months) {
    throw new RangeError('currentCycleIndex out of range');
  }
  const pCurrentMinor = allocations[currentCycleIndex];
  const fFutureMinor = allocations.slice(currentCycleIndex + 1).reduce((sum, item) => sum + item, 0n);
  const cCapMinor = paidMinor > refundedMinor ? paidMinor - refundedMinor : 0n;
  const remainingBaseUnits = baseGrantedUnits > baseConsumedUnits ? baseGrantedUnits - baseConsumedUnits : 0n;
  const currentReturn = baseGrantedUnits === 0n
    ? 0n
    : (pCurrentMinor * remainingBaseUnits) / baseGrantedUnits;
  const rawReturn = fFutureMinor + currentReturn;
  const returnMinor = rawReturn < cCapMinor ? rawReturn : cCapMinor;
  return {
    pCurrentMinor,
    fFutureMinor,
    bBaseUnits: baseGrantedUnits,
    cCapMinor,
    returnMinor,
  };
}

function computeReturnFromStoredParams({ pCurrentMinor, fFutureMinor, bBaseUnits, cCapMinor, consumedUnits }) {
  const remaining = bBaseUnits > consumedUnits ? bBaseUnits - consumedUnits : 0n;
  const curReturn = bBaseUnits === 0n ? 0n : (pCurrentMinor * remaining) / bBaseUnits;
  const total = fFutureMinor + curReturn;
  return total < cCapMinor ? total : cCapMinor;
}

async function resolveTargetCard(connection, { appId, accountKind, region, currency, targetTierId, targetVersionId }) {
  const version = targetVersionId
    ? (await connection.query(`
        SELECT id, version_code AS "versionCode", app_id AS "appId", account_kind AS "accountKind",
          region, currency, effective_at AS "effectiveAt", cards_snapshot AS "cardsSnapshot"
        FROM commerce_versions WHERE id = $1 AND app_id = $2 AND region = $3`, [targetVersionId, appId, region])).rows[0]
    : await resolveCommerceVersion(connection, { appId, accountKind, region, at: new Date() });
  if (!version || version.accountKind !== accountKind || version.currency !== currency) {
    throw new BillingError('commerce_version_not_found', 404);
  }
  const targetCard = version.cardsSnapshot?.find((card) => card.tierId === targetTierId) ?? null;
  if (!targetCard || targetCard.productType !== 'membership') {
    throw new BillingError('product_not_found', 404);
  }
  return { version, targetCard };
}

function assertPersonalUpgradeEligible(activeSub, targetCard) {
  const currentRank = Number(activeSub.tier_rank);
  const targetRank = Number(targetCard.tierRank ?? 0);
  const targetCycle = targetCard.rule?.billingCycle;
  const higherTier = targetRank > currentRank;
  const monthlyToYearlySameTier = targetRank === currentRank
    && activeSub.billing_cycle === 'monthly'
    && targetCycle === 'yearly'
    && targetCard.tierId !== activeSub.tier_id;
  if (!higherTier && !monthlyToYearlySameTier) {
    throw new BillingError('upgrade_not_allowed', 400);
  }
}

async function readUpgradeState(connection, { accountId, appId, targetTierId, targetVersionId, region, currency }) {
  const { rows: [activeSub] } = await connection.query(`
    SELECT * FROM app_subscriptions
    WHERE account_id = $1 AND app_id = $2 AND status = 'active'
    FOR UPDATE`, [accountId, appId]);
  if (!activeSub) throw new BillingError('subscription_not_found', 404);

  const { rows: [oldOrder] } = await connection.query(
    'SELECT * FROM commerce_orders WHERE id = $1 FOR UPDATE', [activeSub.source_order_id],
  );
  if (!oldOrder) throw new BillingError('order_not_found', 404);

  const { version, targetCard } = await resolveTargetCard(connection, {
    appId, accountKind: 'personal', region, currency, targetTierId, targetVersionId,
  });
  assertPersonalUpgradeEligible(activeSub, targetCard);

  const { rows: [oldBaseBatch] } = await connection.query(`
    SELECT * FROM credit_batches
    WHERE subscription_id = $1 AND source_type = 'plan_base' AND grant_cycle_index = $2
    ORDER BY created_at DESC LIMIT 1
    FOR UPDATE`, [activeSub.id, activeSub.current_grant_cycle_index]);

  const months = activeSub.billing_cycle === 'yearly' ? 12 : 1;
  const bBaseUnits = oldBaseBatch ? BigInt(oldBaseBatch.granted_units) : 0n;
  const settledUnits = oldBaseBatch ? BigInt(oldBaseBatch.settled_units) : 0n;
  const heldUnits = oldBaseBatch ? BigInt(oldBaseBatch.held_units) : 0n;
  const uUpperUnits = settledUnits + heldUnits;

  const proration = computeUpgradeProration({
    paidMinor: BigInt(oldOrder.paid_minor),
    refundedMinor: BigInt(oldOrder.refunded_minor),
    months,
    currentCycleIndex: activeSub.current_grant_cycle_index,
    baseGrantedUnits: bBaseUnits,
    baseConsumedUnits: uUpperUnits,
  });

  return {
    activeSub,
    oldOrder,
    oldBaseBatch: oldBaseBatch ?? null,
    version,
    targetCard,
    uUpperUnits,
    heldUnits,
    ...proration,
  };
}

export async function quotePersonalUpgrade(pool, {
  accountId, appId, targetTierId, targetVersionId = null, region, currency,
}) {
  if (!isUuid(accountId) || !isUuid(appId) || typeof targetTierId !== 'string') {
    throw new BillingError('invalid_upgrade_request', 400);
  }
  return transaction(pool, async (connection) => {
    const { rows: [account] } = await connection.query(
      'SELECT kind FROM accounts WHERE id = $1', [accountId],
    );
    if (!account || account.kind !== 'personal') throw new BillingError('personal_account_required', 400);

    const state = await readUpgradeState(connection, {
      accountId, appId, targetTierId, targetVersionId, region, currency,
    });
    const newPriceMinor = BigInt(state.targetCard.rule.priceMinor);
    return {
      appId,
      oldSubscriptionId: state.activeSub.id,
      oldTierId: state.activeSub.tier_id,
      targetTierId: state.targetCard.tierId,
      targetVersionId: state.version.id,
      newPriceMinor: newPriceMinor.toString(),
      pCurrentMinor: state.pCurrentMinor.toString(),
      fFutureMinor: state.fFutureMinor.toString(),
      bBaseUnits: state.bBaseUnits.toString(),
      uUpperUnits: state.uUpperUnits.toString(),
      cCapMinor: state.cCapMinor.toString(),
      estimatedReturnNowMinor: state.returnMinor.toString(),
      hasInFlightHolds: state.heldUnits > 0n,
    };
  });
}

async function reclaimOldPlanBatches(connection, oldSubscriptionId, upgradeId) {
  const { rows: remainingBatches } = await connection.query(`
    SELECT id, account_id, app_id, available_units
    FROM credit_batches
    WHERE subscription_id = $1 AND source_type IN ('plan_base', 'plan_bonus') AND available_units > 0
    ORDER BY id ASC
    FOR UPDATE`, [oldSubscriptionId]);
  for (const batch of remainingBatches) {
    const avail = BigInt(batch.available_units);
    if (avail <= 0n) continue;
    await connection.query(`
      UPDATE credit_batches
      SET available_units = 0, expired_units = expired_units + $1, frozen_for_new_holds = true
      WHERE id = $2`, [avail.toString(), batch.id]);
    await connection.query(`
      INSERT INTO credit_ledger_entries (
        id, batch_id, account_id, app_id, entry_type, delta_available_units,
        delta_held_units, delta_settled_units, delta_expired_units, ref_id, idempotency_key
      ) VALUES ($1, $2, $3, $4, 'upgrade_reclaim', $5, 0, 0, $6, $7, $8)
      ON CONFLICT (idempotency_key) DO NOTHING`, [
      randomUUID(), batch.id, batch.account_id, batch.app_id, (-avail).toString(), avail.toString(),
      upgradeId, `reclaim:${upgradeId}:${batch.id}`,
    ]);
  }
}

export async function finalizeUpgradeIfReady(connection, batchId) {
  const { rows: [batch] } = await connection.query(
    'SELECT id, subscription_id FROM credit_batches WHERE id = $1', [batchId],
  );
  if (!batch?.subscription_id) return null;

  const { rows: [upgrade] } = await connection.query(`
    SELECT * FROM subscription_upgrades
    WHERE old_subscription_id = $1 AND finalized_at IS NULL
    FOR UPDATE`, [batch.subscription_id]);
  if (!upgrade) return null;

  const { rows: [heldRow] } = await connection.query(`
    SELECT COALESCE(sum(held_units), 0)::text AS total_held
    FROM credit_batches
    WHERE subscription_id = $1 AND source_type IN ('plan_base', 'plan_bonus')`,
  [upgrade.old_subscription_id]);
  if (BigInt(heldRow.total_held) > 0n) return null;

  let settledUnits = 0n;
  if (upgrade.old_current_batch_id) {
    const { rows: [baseBatch] } = await connection.query(
      'SELECT settled_units FROM credit_batches WHERE id = $1 FOR UPDATE', [upgrade.old_current_batch_id],
    );
    if (baseBatch) settledUnits = BigInt(baseBatch.settled_units);
  }

  const finalReturn = computeReturnFromStoredParams({
    pCurrentMinor: BigInt(upgrade.p_current_minor),
    fFutureMinor: BigInt(upgrade.f_future_minor),
    bBaseUnits: BigInt(upgrade.b_base_units),
    cCapMinor: BigInt(upgrade.c_cap_minor),
    consumedUnits: settledUnits,
  });

  const returnedSoFar = BigInt(upgrade.returned_so_far_minor);
  let deltaMinor = finalReturn > returnedSoFar ? finalReturn - returnedSoFar : 0n;

  const { rows: [oldOrder] } = await connection.query(
    'SELECT * FROM commerce_orders WHERE id = $1 FOR UPDATE', [upgrade.old_order_id],
  );
  const { rows: [wallet] } = await connection.query(
    'SELECT * FROM wallet_accounts WHERE account_id = $1 FOR UPDATE', [upgrade.account_id],
  );
  const orderRemainingCap = BigInt(oldOrder.paid_minor) - BigInt(oldOrder.refunded_minor);
  if (deltaMinor > orderRemainingCap) deltaMinor = orderRemainingCap;

  if (deltaMinor > 0n) {
    const nextAvailable = BigInt(wallet.available_minor) + deltaMinor;
    await connection.query(`
      UPDATE wallet_accounts SET available_minor = $1, updated_at = statement_timestamp()
      WHERE account_id = $2`, [nextAvailable.toString(), wallet.account_id]);
    await connection.query(`
      INSERT INTO cash_ledger_entries (
        id, account_id, region, currency, entry_type, delta_minor, balance_after_minor,
        arrears_after_minor, ref_type, ref_id, idempotency_key
      ) VALUES ($1, $2, $3, $4, 'upgrade_return_adjust', $5, $6, $7, 'upgrade_record', $8, $9)`, [
      randomUUID(), wallet.account_id, wallet.region, wallet.currency, deltaMinor.toString(),
      nextAvailable.toString(), wallet.arrears_minor, upgrade.id, `upgrade:adjust:${upgrade.id}`,
    ]);
    await connection.query(
      'UPDATE commerce_orders SET refunded_minor = refunded_minor + $1 WHERE id = $2',
      [deltaMinor.toString(), oldOrder.id],
    );
    await connection.query(
      'UPDATE subscription_upgrades SET returned_so_far_minor = returned_so_far_minor + $1 WHERE id = $2',
      [deltaMinor.toString(), upgrade.id],
    );
  }

  await reclaimOldPlanBatches(connection, upgrade.old_subscription_id, upgrade.id);
  await connection.query(
    'UPDATE subscription_upgrades SET finalized_at = statement_timestamp() WHERE id = $1', [upgrade.id],
  );
  return { upgradeId: upgrade.id, adjustmentMinor: deltaMinor.toString() };
}

registerBatchSettledHook(finalizeUpgradeIfReady);

export async function executePersonalUpgrade(pool, {
  accountId, userId, appId, targetTierId, targetVersionId = null, region, currency, autoRenew = false,
  lagoEnabled = false,
}) {
  if (!isUuid(accountId) || !isUuid(userId) || !isUuid(appId) || typeof targetTierId !== 'string') {
    throw new BillingError('invalid_upgrade_request', 400);
  }
  return transaction(pool, async (connection) => {
    const { rows: [account] } = await connection.query(
      'SELECT id, kind, owner_id FROM accounts WHERE id = $1 FOR UPDATE', [accountId],
    );
    if (!account || account.kind !== 'personal' || account.owner_id !== userId) {
      throw new BillingError('personal_account_required', 403);
    }

    const state = await readUpgradeState(connection, {
      accountId, appId, targetTierId, targetVersionId, region, currency,
    });
    const newPriceMinor = BigInt(state.targetCard.rule.priceMinor);
    const { rows: [wallet] } = await connection.query(
      'SELECT * FROM wallet_accounts WHERE account_id = $1 FOR UPDATE', [accountId],
    );
    if (!wallet || wallet.region !== region || wallet.currency !== currency) {
      throw new BillingError('wallet_not_found', 404);
    }
    const available = BigInt(wallet.available_minor);
    if (BigInt(wallet.arrears_minor) > 0n || wallet.frozen_reason || available < newPriceMinor) {
      throw new BillingError('insufficient_funds', 402, {
        amountMinor: newPriceMinor.toString(),
        availableMinor: available.toString(),
        pendingCheckout: {
          accountId, appId, versionId: state.version.id, tierId: targetTierId,
          quantity: 1, orderType: 'upgrade', autoRenew: Boolean(autoRenew),
        },
      });
    }

    // 1. Freeze old subscription plan batches for new holds (independent credit_pack batches remain untouched!)
    await connection.query(`
      UPDATE credit_batches SET frozen_for_new_holds = true
      WHERE subscription_id = $1 AND source_type IN ('plan_base', 'plan_bonus')`,
    [state.activeSub.id]);
    await connection.query(
      "UPDATE app_subscriptions SET status = 'superseded', updated_at = statement_timestamp() WHERE id = $1",
      [state.activeSub.id],
    );

    // 2. Debit wallet for full new plan price & create upgrade order
    const upgradeId = randomUUID();
    const newOrderId = randomUUID();
    const newSubId = randomUUID();
    const snapshot = {
      appId,
      accountId,
      accountKind: 'personal',
      region,
      currency,
      versionId: state.version.id,
      versionCode: state.version.versionCode,
      card: state.targetCard,
      quantity: 1,
      unitPriceMinor: newPriceMinor.toString(),
      autoRenew: Boolean(autoRenew),
      upgradeFromSubscriptionId: state.activeSub.id,
      upgradeFromOrderId: state.oldOrder.id,
      proration: {
        pCurrentMinor: state.pCurrentMinor.toString(),
        fFutureMinor: state.fFutureMinor.toString(),
        bBaseUnits: state.bBaseUnits.toString(),
        uUpperUnits: state.uUpperUnits.toString(),
        cCapMinor: state.cCapMinor.toString(),
        returnNowMinor: state.returnMinor.toString(),
      },
    };
    const { rows: [newOrder] } = await connection.query(`
      INSERT INTO commerce_orders (
        id, order_no, account_id, buyer_user_id, app_id, order_type, commerce_version_id,
        card_tier_id, quantity, region, currency, paid_minor, snapshot_json, status, lago_sync_status
      ) VALUES ($1, $2, $3, $4, $5, 'upgrade', $6, $7, 1, $8, $9, $10, $11::jsonb, 'fulfilled', $12)
      RETURNING *`, [
      newOrderId, orderNo(), accountId, userId, appId, state.version.id,
      state.targetCard.tierId, region, currency, newPriceMinor.toString(), jsonSafe(snapshot),
      lagoEnabled ? 'pending' : 'skipped',
    ]);
    await clearPendingCheckout(connection, accountId);
    await enqueueLagoInvoiceSync(connection, newOrderId, lagoEnabled);

    const afterPurchase = available - newPriceMinor;
    await connection.query(`
      INSERT INTO cash_ledger_entries (
        id, account_id, region, currency, entry_type, delta_minor, balance_after_minor,
        arrears_after_minor, ref_type, ref_id, idempotency_key
      ) VALUES ($1, $2, $3, $4, 'purchase', $5, $6, $7, 'commerce_order', $8, $9)`, [
      randomUUID(), accountId, region, currency, (-newPriceMinor).toString(), afterPurchase.toString(),
      wallet.arrears_minor, newOrderId, `purchase:${newOrderId}`,
    ]);

    // 3. Return immediate proration R_now to wallet & record on old order
    let finalAvailable = afterPurchase;
    if (state.returnMinor > 0n) {
      finalAvailable = afterPurchase + state.returnMinor;
      await connection.query(`
        INSERT INTO cash_ledger_entries (
          id, account_id, region, currency, entry_type, delta_minor, balance_after_minor,
          arrears_after_minor, ref_type, ref_id, idempotency_key
        ) VALUES ($1, $2, $3, $4, 'upgrade_return', $5, $6, $7, 'upgrade_record', $8, $9)`, [
        randomUUID(), accountId, region, currency, state.returnMinor.toString(), finalAvailable.toString(),
        wallet.arrears_minor, upgradeId, `upgrade:return:${upgradeId}`,
      ]);
      await connection.query(
        'UPDATE commerce_orders SET refunded_minor = refunded_minor + $1 WHERE id = $2',
        [state.returnMinor.toString(), state.oldOrder.id],
      );
    }

    await connection.query(
      'UPDATE wallet_accounts SET available_minor = $1, updated_at = statement_timestamp() WHERE account_id = $2',
      [finalAvailable.toString(), accountId],
    );

    // 4. Create new active subscription + cycle 0 credit batches
    const cycle = state.targetCard.rule.billingCycle === 'yearly' ? 'yearly' : 'monthly';
    const { rows: [newSub] } = await connection.query(`
      INSERT INTO app_subscriptions (
        id, account_id, app_id, commerce_version_id, source_order_id, tier_id, tier_rank,
        billing_cycle, seat_count, status, auto_renew, authorized_renew_price_minor,
        period_start, period_end
      ) VALUES (
        $1, $2, $3, $4, $5, $6, $7, $8, 1, 'active', $9, $10,
        statement_timestamp(), statement_timestamp() + CASE WHEN $8 = 'yearly' THEN interval '1 year' ELSE interval '1 month' END
      ) RETURNING *, statement_timestamp() + interval '1 month' AS grant_expires_at`, [
      newSubId, accountId, appId, state.version.id, newOrderId,
      state.targetCard.tierId, state.targetCard.tierRank ?? 0, cycle,
      Boolean(autoRenew), newPriceMinor.toString(),
    ]);

    await insertCreditBatch(connection, {
      accountId, appId, sourceType: 'plan_base', sourceOrderId: newOrderId,
      subscriptionId: newSubId, grantCycleIndex: 0,
      units: unitsForCredits(state.targetCard.rule.baseCredits, 1),
      expiresAt: newSub.grant_expires_at, suffix: 'base:0',
    });
    await insertCreditBatch(connection, {
      accountId, appId, sourceType: 'plan_bonus', sourceOrderId: newOrderId,
      subscriptionId: newSubId, grantCycleIndex: 0,
      units: unitsForCredits(state.targetCard.rule.bonusCredits, 1),
      expiresAt: newSub.grant_expires_at, suffix: 'bonus:0',
    });

    // 5. Record subscription_upgrades & finalize immediately if zero in-flight holds
    const { rows: [heldRow] } = await connection.query(`
      SELECT COALESCE(sum(held_units), 0)::text AS total_held
      FROM credit_batches
      WHERE subscription_id = $1 AND source_type IN ('plan_base', 'plan_bonus')`,
    [state.activeSub.id]);
    const hasInFlight = BigInt(heldRow.total_held) > 0n;

    await connection.query(`
      INSERT INTO subscription_upgrades (
        id, account_id, app_id, old_subscription_id, old_order_id, new_subscription_id,
        new_order_id, old_current_batch_id, p_current_minor, f_future_minor,
        b_base_units, c_cap_minor, returned_so_far_minor, finalized_at
      ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, ${hasInFlight ? 'NULL' : 'statement_timestamp()'})`, [
      upgradeId, accountId, appId, state.activeSub.id, state.oldOrder.id, newSubId,
      newOrderId, state.oldBaseBatch?.id ?? null, state.pCurrentMinor.toString(),
      state.fFutureMinor.toString(), state.bBaseUnits.toString(), state.cCapMinor.toString(),
      state.returnMinor.toString(),
    ]);

    if (!hasInFlight) {
      await reclaimOldPlanBatches(connection, state.activeSub.id, upgradeId);
    }

    return {
      upgradeId,
      finalized: !hasInFlight,
      returnedMinor: state.returnMinor.toString(),
      order: mapOrder(newOrder),
      subscription: mapSubscription(newSub),
      wallet: { availableMinor: finalAvailable.toString(), arrearsMinor: wallet.arrears_minor },
    };
  });
}
