import { isUuid } from '../database.js';
import { BillingError, sendBillingError } from '../billing/errors.js';
import {
  createPaymentOrder,
  listCashLedger,
  listPaymentOrders,
  readWallet,
  serializePayment,
  serializeWallet,
  settlePayment,
} from '../billing/wallet.js';
import {
  verifyAlipayRsa2,
  verifyStripeWebhook,
  verifyWechatV3,
} from './verifiers.js';

async function activeAccount(pool, session) {
  const { rows: [account] } = await pool.query(`
    SELECT a.id, a.kind, a.owner_id, m.role, COALESCE(m.can_recharge, false) AS can_recharge
    FROM accounts a
    LEFT JOIN enterprise_members m ON m.account_id = a.id AND m.user_id = $2
    WHERE a.id = $1 AND (a.kind = 'personal' AND a.owner_id = $2 OR a.kind = 'enterprise' AND m.user_id IS NOT NULL)`,
  [session.activeAccountId, session.userId]);
  if (!account) throw new BillingError('account_forbidden', 403);
  return account;
}

function defaultProvider(region) {
  return region === 'us' ? 'stripe' : 'wechat';
}

function parseYuanToMinor(value) {
  if (typeof value !== 'string' && typeof value !== 'number') return null;
  const text = String(value).trim();
  if (!/^(?:0|[1-9]\d*)(?:\.\d{1,2})?$/.test(text)) return null;
  const [whole, frac = ''] = text.split('.');
  return (BigInt(whole) * 100n + BigInt(frac.padEnd(2, '0'))).toString();
}

function normalizeWebhookSettlement(provider, body, decrypted, region, currency) {
  if (provider === 'wechat') {
    const src = decrypted ?? body ?? {};
    return {
      paymentOrderId: src.out_trade_no ?? src.paymentOrderId ?? body?.paymentOrderId,
      merchant: src.mchid ?? src.merchantId ?? body?.merchantId,
      providerTradeNo: src.transaction_id ?? src.providerTradeNo ?? body?.providerTradeNo,
      amountMinor: src.amount?.total ?? src.amountMinor ?? body?.amountMinor,
      region: src.region ?? body?.region ?? region,
      currency: src.amount?.currency ?? src.currency ?? body?.currency ?? currency,
    };
  }
  if (provider === 'alipay') {
    const src = body ?? {};
    return {
      paymentOrderId: src.out_trade_no ?? src.paymentOrderId,
      merchant: src.seller_id ?? src.app_id ?? src.merchantId,
      providerTradeNo: src.trade_no ?? src.providerTradeNo,
      amountMinor: src.amountMinor ?? parseYuanToMinor(src.total_amount),
      region: src.region ?? region,
      currency: src.currency ?? currency,
    };
  }
  if (provider === 'stripe') {
    const obj = body?.data?.object;
    if (obj && typeof obj === 'object') {
      return {
        paymentOrderId: obj.metadata?.paymentOrderId ?? obj.client_reference_id ?? body?.paymentOrderId,
        merchant: obj.metadata?.merchantId ?? body?.merchantId ?? `${region}-default-merchant`,
        providerTradeNo: obj.payment_intent ?? obj.id ?? body?.providerTradeNo,
        amountMinor: obj.amount_total ?? obj.amount_received ?? obj.amount ?? body?.amountMinor,
        region: obj.metadata?.region ?? body?.region ?? region,
        currency: typeof obj.currency === 'string' ? obj.currency.toUpperCase() : (body?.currency ?? currency),
      };
    }
    return {
      paymentOrderId: body?.paymentOrderId,
      merchant: body?.merchantId,
      providerTradeNo: body?.providerTradeNo,
      amountMinor: body?.amountMinor,
      region: body?.region ?? region,
      currency: body?.currency ?? currency,
    };
  }
  return {
    paymentOrderId: body?.paymentOrderId,
    merchant: body?.merchantId,
    providerTradeNo: body?.providerTradeNo,
    amountMinor: body?.amountMinor,
    region: body?.region ?? region,
    currency: body?.currency ?? currency,
  };
}

export function registerPaymentRoutes(app, pool, auth, write, { region, currency, paymentConfig = {} }) {
  async function createRecharge(request, reply) {
    try {
      const account = await activeAccount(pool, request.auth);
      if (account.kind === 'enterprise' && !account.can_recharge) throw new BillingError('recharge_forbidden', 403);
      const body = request.body ?? {};
      const payment = await createPaymentOrder(pool, {
        accountId: account.id,
        initiatorUserId: request.auth.userId,
        region,
        currency,
        provider: body.provider ?? defaultProvider(region),
        merchant: body.merchantId ?? `${region}-default-merchant`,
        amountMinor: body.amountMinor,
        pendingCheckout: body.pendingCheckout ?? null,
        returnUrl: body.returnUrl ?? null,
      });
      return reply.code(201).send({ paymentOrder: serializePayment(payment) });
    } catch (error) {
      return sendBillingError(reply, error);
    }
  }

  app.get('/api/wallet', auth, async (request, reply) => {
    try {
      const account = await activeAccount(pool, request.auth);
      const wallet = await readWallet(pool, account.id);
      return { accountId: account.id, wallet: serializeWallet(wallet) };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
  app.get('/api/billing/wallet', auth, async (request, reply) => {
    try {
      const account = await activeAccount(pool, request.auth);
      return { accountId: account.id, wallet: serializeWallet(await readWallet(pool, account.id)) };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
  app.get('/api/wallet/payment-orders', auth, async (request, reply) => {
    try {
      const account = await activeAccount(pool, request.auth);
      const orders = await listPaymentOrders(pool, account.id);
      return { paymentOrders: orders.map(serializePayment) };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
  app.get('/api/wallet/ledger', auth, async (request, reply) => {
    try {
      const account = await activeAccount(pool, request.auth);
      return { entries: await listCashLedger(pool, account.id) };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
  app.post('/api/wallet/recharges', { ...write, bodyLimit: 65_536 }, createRecharge);
  app.post('/api/billing/recharges', { ...write, bodyLimit: 65_536 }, createRecharge);

  async function readPayment(request, reply) {
    if (!isUuid(request.params.paymentOrderId)) return reply.code(400).send({ error: 'invalid_payment_order' });
    const account = await activeAccount(pool, request.auth);
    const { rows: [payment] } = await pool.query(
      'SELECT * FROM payment_orders WHERE id = $1 AND account_id = $2', [request.params.paymentOrderId, account.id],
    );
    if (!payment) return reply.code(404).send({ error: 'payment_order_not_found' });
    return {
      paymentOrder: serializePayment({
        id: payment.id,
        accountId: payment.account_id,
        initiatorUserId: payment.initiator_user_id,
        region: payment.region,
        currency: payment.currency,
        provider: payment.provider,
        merchantId: payment.merchant_id,
        providerTradeNo: payment.provider_trade_no,
        amountMinor: BigInt(payment.amount_minor),
        refundableMinor: BigInt(payment.refundable_minor),
        status: payment.status,
        pendingCheckout: payment.pending_checkout_json,
        returnUrl: payment.return_url,
        paidAt: payment.paid_at,
        createdAt: payment.created_at,
      }),
    };
  }
  app.get('/api/wallet/payment-orders/:paymentOrderId', auth, readPayment);
  app.get('/api/billing/payment-orders/:paymentOrderId', auth, readPayment);

  async function settleSandbox(request, reply) {
    try {
      if (!isUuid(request.params.paymentOrderId)) throw new BillingError('invalid_payment_order', 400);
      const account = await activeAccount(pool, request.auth);
      const { rows: [payment] } = await pool.query(
        'SELECT id, provider, merchant_id, amount_minor FROM payment_orders WHERE id = $1 AND account_id = $2',
        [request.params.paymentOrderId, account.id],
      );
      if (!payment) throw new BillingError('payment_order_not_found', 404);
      const body = request.body ?? {};
      const result = await settlePayment(pool, {
        paymentOrderId: payment.id,
        provider: body.provider ?? payment.provider,
        merchant: body.merchantId ?? payment.merchant_id,
        providerTradeNo: body.providerTradeNo,
        amountMinor: body.amountMinor ?? payment.amount_minor,
        region,
        currency,
      });
      return { paymentOrder: serializePayment(result.payment), wallet: serializeWallet(result.wallet), duplicate: result.duplicate };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  }
  app.post('/api/wallet/payment-orders/:paymentOrderId/sandbox-settle', { ...write, bodyLimit: 65_536 }, settleSandbox);
  app.post('/api/billing/payment-orders/:paymentOrderId/sandbox-settle', { ...write, bodyLimit: 65_536 }, settleSandbox);

  app.post('/api/webhooks/payments/:provider', { bodyLimit: 65_536 }, async (request, reply) => {
    try {
      const provider = request.params.provider;
      let decrypted = null;
      const providerConfig = paymentConfig?.[provider];

      if (providerConfig) {
        let verified = { valid: false, reason: 'unsupported_provider' };
        if (provider === 'wechat') {
          verified = verifyWechatV3({
            headers: request.headers,
            rawBody: request.rawBody ?? JSON.stringify(request.body ?? {}),
            platformPublicKeys: providerConfig.platformPublicKeys,
            apiV3Key: providerConfig.apiV3Key,
            maxSkewSeconds: providerConfig.maxSkewSeconds ?? 300,
          });
          decrypted = verified.decrypted ?? null;
        } else if (provider === 'alipay') {
          verified = verifyAlipayRsa2({
            params: request.body,
            alipayPublicKey: providerConfig.alipayPublicKey,
          });
        } else if (provider === 'stripe') {
          verified = verifyStripeWebhook({
            signatureHeader: request.headers['stripe-signature'],
            rawBody: request.rawBody ?? JSON.stringify(request.body ?? {}),
            webhookSecret: providerConfig.webhookSecret,
            toleranceSeconds: providerConfig.toleranceSeconds ?? 300,
          });
        }
        if (!verified.valid) {
          return reply.code(401).send({ error: 'invalid_signature', reason: verified.reason });
        }
      } else if (!paymentConfig?.allowUnsignedTestWebhooks) {
        return reply.code(503).send({ error: 'payment_provider_unconfigured' });
      }

      const normalized = normalizeWebhookSettlement(provider, request.body, decrypted, region, currency);
      const result = await settlePayment(pool, {
        ...normalized,
        provider,
      });
      return { paymentOrder: serializePayment(result.payment), wallet: serializeWallet(result.wallet), duplicate: result.duplicate };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
}
