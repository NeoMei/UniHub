import { randomUUID } from 'node:crypto';
import { isUuid, transaction } from '../database.js';
import { BillingError } from './errors.js';
import { parseAmountMinor, providerAllowedForRegion } from './validation.js';

function paymentTradeNo(value) {
  return typeof value === 'string' && /^[A-Za-z0-9._:-]{1,160}$/.test(value) ? value : null;
}

function merchantId(value) {
  return typeof value === 'string' && /^[A-Za-z0-9._:-]{1,120}$/.test(value) ? value : null;
}

function mapWallet(row) {
  if (!row) return null;
  return {
    accountId: row.account_id,
    region: row.region,
    currency: row.currency,
    availableMinor: BigInt(row.available_minor),
    arrearsMinor: BigInt(row.arrears_minor),
    frozenReason: row.frozen_reason,
    updatedAt: row.updated_at,
  };
}

function mapPayment(row) {
  if (!row) return null;
  return {
    id: row.id,
    accountId: row.account_id,
    initiatorUserId: row.initiator_user_id,
    region: row.region,
    currency: row.currency,
    provider: row.provider,
    merchantId: row.merchant_id,
    providerTradeNo: row.provider_trade_no,
    amountMinor: BigInt(row.amount_minor),
    refundableMinor: BigInt(row.refundable_minor),
    status: row.status,
    pendingCheckout: row.pending_checkout_json,
    returnUrl: row.return_url,
    paidAt: row.paid_at,
    createdAt: row.created_at,
  };
}

function jsonSafe(value) {
  return JSON.stringify(value, (_, item) => typeof item === 'bigint' ? item.toString() : item);
}

export async function readWallet(pool, accountId) {
  const { rows: [wallet] } = await pool.query(
    'SELECT * FROM wallet_accounts WHERE account_id = $1', [accountId],
  );
  return mapWallet(wallet);
}

export async function createPaymentOrder(pool, {
  accountId, initiatorUserId, region, currency, provider, merchant,
  amountMinor, pendingCheckout = null, returnUrl = null,
}) {
  const amount = parseAmountMinor(amountMinor);
  const cleanMerchant = merchantId(merchant);
  if (!amount || amount <= 0n || !providerAllowedForRegion(region, currency, provider) || !cleanMerchant) {
    throw new BillingError('invalid_payment_order', 400);
  }
  if (pendingCheckout !== null && (!pendingCheckout || typeof pendingCheckout !== 'object' || Array.isArray(pendingCheckout))) {
    throw new BillingError('invalid_pending_checkout', 400);
  }
  if (returnUrl !== null && (typeof returnUrl !== 'string' || returnUrl.length > 2048 || returnUrl.includes('\0'))) {
    throw new BillingError('invalid_return_url', 400);
  }

  return transaction(pool, async (connection) => {
    const { rows: [account] } = await connection.query(`
      INSERT INTO wallet_accounts (account_id, region, currency)
      SELECT id, $2, $3 FROM accounts WHERE id = $1
      ON CONFLICT (account_id) DO NOTHING
      RETURNING account_id`, [accountId, region, currency]);
    if (!account) {
      const { rowCount } = await connection.query('SELECT 1 FROM wallet_accounts WHERE account_id = $1', [accountId]);
      if (!rowCount) throw new BillingError('account_not_found', 404);
    }
    const { rows: [created] } = await connection.query(`
      INSERT INTO payment_orders (
        id, account_id, initiator_user_id, region, currency, provider, merchant_id,
        amount_minor, status, pending_checkout_json, return_url
      ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending', $9::jsonb, $10)
      RETURNING *`, [
      randomUUID(), accountId, initiatorUserId, region, currency, provider, cleanMerchant,
      amount.toString(), pendingCheckout === null ? null : jsonSafe(pendingCheckout), returnUrl,
    ]);
    if (!created) throw new BillingError('account_not_found', 404);
    return mapPayment(created);
  });
}

export async function settlePayment(pool, {
  paymentOrderId, provider, merchant, providerTradeNo: rawTradeNo,
  amountMinor, region, currency,
}) {
  const tradeNo = paymentTradeNo(rawTradeNo);
  const amount = parseAmountMinor(amountMinor);
  const cleanMerchant = merchantId(merchant);
  if (!paymentOrderId || !tradeNo || !amount || amount <= 0n
    || !providerAllowedForRegion(region, currency, provider) || !cleanMerchant) {
    throw new BillingError('invalid_payment_callback', 400);
  }

  return transaction(pool, async (connection) => {
    const { rows: [payment] } = await connection.query(
      'SELECT * FROM payment_orders WHERE id = $1 FOR UPDATE', [paymentOrderId],
    );
    if (!payment) throw new BillingError('payment_order_not_found', 404);
    if (payment.provider !== provider || payment.merchant_id !== cleanMerchant
      || payment.region !== region || payment.currency !== currency
      || BigInt(payment.amount_minor) !== amount) {
      throw new BillingError('payment_callback_mismatch', 400);
    }
    if (payment.status === 'succeeded') {
      if (payment.provider_trade_no !== tradeNo) throw new BillingError('payment_already_settled', 409);
      return { payment: mapPayment(payment), wallet: await readWallet(connection, payment.account_id), duplicate: true };
    }
    if (payment.status !== 'pending') throw new BillingError('payment_not_pending', 409);

    const { rows: [existingTrade] } = await connection.query(`
      SELECT id FROM payment_orders
      WHERE provider = $1 AND merchant_id = $2 AND provider_trade_no = $3
      FOR UPDATE`, [provider, cleanMerchant, tradeNo]);
    if (existingTrade && existingTrade.id !== payment.id) throw new BillingError('payment_trade_reused', 409);

    const { rows: [wallet] } = await connection.query(
      'SELECT * FROM wallet_accounts WHERE account_id = $1 FOR UPDATE', [payment.account_id],
    );
    if (!wallet) throw new BillingError('wallet_not_found', 404);

    const currentAvailable = BigInt(wallet.available_minor);
    const currentArrears = BigInt(wallet.arrears_minor);
    const arrearsApplied = currentArrears < amount ? currentArrears : amount;
    const availableIncrease = amount - arrearsApplied;
    const nextArrears = currentArrears - arrearsApplied;
    const nextAvailable = currentAvailable + availableIncrease;
    const nextFrozenReason = nextArrears === 0n ? null : wallet.frozen_reason ?? 'arrears';
    const { rows: [updatedWallet] } = await connection.query(`
      UPDATE wallet_accounts
      SET available_minor = $1, arrears_minor = $2, frozen_reason = $3, updated_at = statement_timestamp()
      WHERE account_id = $4
      RETURNING *`, [nextAvailable.toString(), nextArrears.toString(), nextFrozenReason, payment.account_id]);

    await connection.query(`
      INSERT INTO cash_ledger_entries (
        id, account_id, region, currency, entry_type, delta_minor,
        balance_after_minor, arrears_after_minor, ref_type, ref_id, idempotency_key
      ) VALUES ($1, $2, $3, $4, 'recharge', $5, $6, $7, 'payment_order', $8, $9)`, [
      randomUUID(), payment.account_id, payment.region, payment.currency, amount.toString(),
      nextAvailable.toString(), nextArrears.toString(), payment.id, `payment:${payment.id}:succeeded`,
    ]);

    const { rows: [updatedPayment] } = await connection.query(`
      UPDATE payment_orders
      SET provider_trade_no = $1, status = 'succeeded', refundable_minor = amount_minor,
          paid_at = statement_timestamp()
      WHERE id = $2
      RETURNING *`, [tradeNo, payment.id]);
    return { payment: mapPayment(updatedPayment), wallet: mapWallet(updatedWallet), duplicate: false };
  });
}

export function serializeWallet(value) {
  return value === null ? null : {
    ...value,
    availableMinor: value.availableMinor.toString(),
    arrearsMinor: value.arrearsMinor.toString(),
  };
}

export function serializePayment(value) {
  return value === null ? null : {
    ...value,
    amountMinor: value.amountMinor.toString(),
    refundableMinor: value.refundableMinor.toString(),
  };
}

export async function listPaymentOrders(pool, accountId) {
  const { rows } = await pool.query(`
    SELECT * FROM payment_orders WHERE account_id = $1
    ORDER BY created_at DESC LIMIT 100`, [accountId]);
  return rows.map(mapPayment);
}

export async function listCashLedger(pool, accountId) {
  const { rows } = await pool.query(`
    SELECT * FROM cash_ledger_entries WHERE account_id = $1
    ORDER BY created_at DESC, id DESC LIMIT 200`, [accountId]);
  return rows.map((row) => ({
    id: row.id,
    accountId: row.account_id,
    region: row.region,
    currency: row.currency,
    entryType: row.entry_type,
    deltaMinor: BigInt(row.delta_minor).toString(),
    balanceAfterMinor: BigInt(row.balance_after_minor).toString(),
    arrearsAfterMinor: BigInt(row.arrears_after_minor).toString(),
    refType: row.ref_type,
    refId: row.ref_id,
    idempotencyKey: row.idempotency_key,
    createdAt: row.created_at,
  }));
}

export async function refundPaymentOrder(pool, {
  paymentOrderId, amountMinor, operatorUserId, idempotencyKey,
}) {
  const amount = parseAmountMinor(amountMinor);
  const cleanKey = typeof idempotencyKey === 'string' && /^[A-Za-z0-9._:-]{1,120}$/.test(idempotencyKey)
    ? idempotencyKey : null;
  if (!isUuid(paymentOrderId) || !isUuid(operatorUserId) || !amount || amount <= 0n || !cleanKey) {
    throw new BillingError('invalid_refund_request', 400);
  }
  const ledgerKey = `refund:${paymentOrderId}:${cleanKey}`;
  return transaction(pool, async (connection) => {
    const { rowCount: existingLedger } = await connection.query(
      'SELECT 1 FROM cash_ledger_entries WHERE idempotency_key = $1', [ledgerKey],
    );
    const { rows: [payment] } = await connection.query(
      'SELECT * FROM payment_orders WHERE id = $1 FOR UPDATE', [paymentOrderId],
    );
    if (!payment) throw new BillingError('payment_order_not_found', 404);
    const { rows: [wallet] } = await connection.query(
      'SELECT * FROM wallet_accounts WHERE account_id = $1 FOR UPDATE', [payment.account_id],
    );
    if (!wallet) throw new BillingError('wallet_not_found', 404);
    if (existingLedger) {
      return { payment: mapPayment(payment), wallet: mapWallet(wallet), duplicate: true };
    }
    if (payment.status !== 'succeeded') throw new BillingError('payment_not_refundable', 409);
    const refundable = BigInt(payment.refundable_minor);
    const available = BigInt(wallet.available_minor);
    if (refundable < amount) throw new BillingError('refund_exceeds_refundable', 409);
    if (available < amount) throw new BillingError('insufficient_available_for_refund', 409);

    const nextRefundable = refundable - amount;
    const nextStatus = nextRefundable === 0n ? 'refunded' : 'succeeded';
    const nextAvailable = available - amount;

    const { rows: [updatedWallet] } = await connection.query(`
      UPDATE wallet_accounts
      SET available_minor = $1, updated_at = statement_timestamp()
      WHERE account_id = $2 RETURNING *`, [nextAvailable.toString(), wallet.account_id]);
    const { rows: [updatedPayment] } = await connection.query(`
      UPDATE payment_orders
      SET refundable_minor = $1, status = $2
      WHERE id = $3 RETURNING *`, [nextRefundable.toString(), nextStatus, payment.id]);
    await connection.query(`
      INSERT INTO cash_ledger_entries (
        id, account_id, region, currency, entry_type, delta_minor,
        balance_after_minor, arrears_after_minor, ref_type, ref_id, idempotency_key
      ) VALUES ($1, $2, $3, $4, 'original_refund', $5, $6, $7, 'refund_order', $8, $9)`, [
      randomUUID(), wallet.account_id, payment.region, payment.currency, (-amount).toString(),
      nextAvailable.toString(), wallet.arrears_minor, payment.id, ledgerKey,
    ]);
    return { payment: mapPayment(updatedPayment), wallet: mapWallet(updatedWallet), duplicate: false };
  });
}

export async function recordChargeback(pool, {
  paymentOrderId, amountMinor, disputeId,
}) {
  const amount = parseAmountMinor(amountMinor);
  const cleanDispute = typeof disputeId === 'string' && /^[A-Za-z0-9._:-]{1,120}$/.test(disputeId)
    ? disputeId : null;
  if (!isUuid(paymentOrderId) || !amount || amount <= 0n || !cleanDispute) {
    throw new BillingError('invalid_chargeback_request', 400);
  }
  const ledgerKey = `chargeback:${paymentOrderId}:${cleanDispute}`;
  return transaction(pool, async (connection) => {
    const { rowCount: existingLedger } = await connection.query(
      'SELECT 1 FROM cash_ledger_entries WHERE idempotency_key = $1', [ledgerKey],
    );
    const { rows: [payment] } = await connection.query(
      'SELECT * FROM payment_orders WHERE id = $1 FOR UPDATE', [paymentOrderId],
    );
    if (!payment) throw new BillingError('payment_order_not_found', 404);
    const { rows: [wallet] } = await connection.query(
      'SELECT * FROM wallet_accounts WHERE account_id = $1 FOR UPDATE', [payment.account_id],
    );
    if (!wallet) throw new BillingError('wallet_not_found', 404);
    if (existingLedger) {
      return { payment: mapPayment(payment), wallet: mapWallet(wallet), duplicate: true };
    }
    if (!['succeeded', 'refunded'].includes(payment.status)) {
      throw new BillingError('payment_not_chargebackable', 409);
    }
    if (amount > BigInt(payment.amount_minor)) {
      throw new BillingError('chargeback_exceeds_payment', 400);
    }

    const available = BigInt(wallet.available_minor);
    const arrears = BigInt(wallet.arrears_minor);
    const deducted = available < amount ? available : amount;
    const shortfall = amount - deducted;
    const nextAvailable = available - deducted;
    const nextArrears = arrears + shortfall;
    const nextFrozenReason = nextArrears > 0n ? 'chargeback_arrears' : wallet.frozen_reason;

    const { rows: [updatedWallet] } = await connection.query(`
      UPDATE wallet_accounts
      SET available_minor = $1, arrears_minor = $2, frozen_reason = $3, updated_at = statement_timestamp()
      WHERE account_id = $4 RETURNING *`, [nextAvailable.toString(), nextArrears.toString(), nextFrozenReason, wallet.account_id]);
    const { rows: [updatedPayment] } = await connection.query(`
      UPDATE payment_orders
      SET status = 'chargeback', refundable_minor = 0
      WHERE id = $1 RETURNING *`, [payment.id]);
    await connection.query(`
      INSERT INTO cash_ledger_entries (
        id, account_id, region, currency, entry_type, delta_minor,
        balance_after_minor, arrears_after_minor, ref_type, ref_id, idempotency_key
      ) VALUES ($1, $2, $3, $4, 'chargeback_debit', $5, $6, $7, 'payment_order', $8, $9)`, [
      randomUUID(), wallet.account_id, payment.region, payment.currency, (-amount).toString(),
      nextAvailable.toString(), nextArrears.toString(), payment.id, ledgerKey,
    ]);
    return { payment: mapPayment(updatedPayment), wallet: mapWallet(updatedWallet), duplicate: false };
  });
}

export { mapPayment };
