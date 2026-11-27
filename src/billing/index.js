import { isUuid } from '../database.js';
import { BillingError, sendBillingError } from './errors.js';
import {
  creditBalance,
  holdCredits,
  listCreditLedger,
  releaseCredits,
  settleCredits,
} from './credits.js';
import {
  createCheckout,
  listOrders,
  listSubscriptions,
  processPendingOutbox,
  readOrder,
} from './orders.js';
import { runReconciliation } from './reconciliation.js';
import {
  assignEnterpriseSeat,
  claimEnterpriseInvite,
  createEnterpriseInvite,
  listEnterpriseSeats,
  revokeEnterpriseSeat,
  setMemberLimit,
} from './seats.js';
import {
  recordChargeback,
  refundPaymentOrder,
  serializePayment,
  serializeWallet,
} from './wallet.js';

async function activeAccount(pool, session, accountId = session.activeAccountId) {
  const { rows: [account] } = await pool.query(`
    SELECT a.id, a.kind, a.owner_id, m.role, COALESCE(m.can_recharge, false) AS can_recharge,
      COALESCE(m.can_purchase, false) AS can_purchase
    FROM accounts a
    LEFT JOIN enterprise_members m ON m.account_id = a.id AND m.user_id = $2
    WHERE a.id = $1 AND (a.kind = 'personal' AND a.owner_id = $2 OR a.kind = 'enterprise' AND m.user_id IS NOT NULL)`,
  [accountId, session.userId]);
  if (!account) throw new BillingError('account_forbidden', 403);
  return account;
}

function writeResult(reply, result, success = 200) {
  return reply.code(success).send(result);
}

export function registerBillingRoutes(app, pool, auth, write, isPlatformOperator, { region, currency }) {
  app.post('/api/billing/checkout', { ...write, bodyLimit: 65_536 }, async (request, reply) => {
    try {
      const account = await activeAccount(pool, request.auth);
      const result = await createCheckout(pool, {
        accountId: account.id,
        userId: request.auth.userId,
        region,
        currency,
        input: request.body,
      });
      return writeResult(reply, result, 201);
    } catch (error) {
      if (error instanceof BillingError && error.code === 'insufficient_funds') {
        try {
          const account = await activeAccount(pool, request.auth);
          const pending = error.details.pendingCheckout;
          const body = request.body ?? {};
          const payment = await import('./wallet.js').then(({ createPaymentOrder }) => createPaymentOrder(pool, {
            accountId: account.id,
            initiatorUserId: request.auth.userId,
            region,
            currency,
            provider: body.provider ?? (region === 'us' ? 'stripe' : 'wechat'),
            merchant: body.merchantId ?? `${region}-default-merchant`,
            amountMinor: body.rechargeAmountMinor ?? error.details.amountMinor,
            pendingCheckout: pending,
            returnUrl: body.returnUrl ?? null,
          }));
          return reply.code(402).send({
            error: error.code,
            amountMinor: error.details.amountMinor,
            availableMinor: error.details.availableMinor,
            pendingCheckout: pending,
            paymentOrder: serializePayment(payment),
          });
        } catch (recoveryError) {
          return sendBillingError(reply, recoveryError);
        }
      }
      return sendBillingError(reply, error);
    }
  });

  app.get('/api/billing/orders', auth, async (request, reply) => {
    try {
      const account = await activeAccount(pool, request.auth);
      return { orders: await listOrders(pool, account.id) };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
  app.get('/api/billing/orders/:orderId', auth, async (request, reply) => {
    try {
      if (!isUuid(request.params.orderId)) throw new BillingError('invalid_order', 400);
      const account = await activeAccount(pool, request.auth);
      const order = await readOrder(pool, request.params.orderId, account.id);
      if (!order) throw new BillingError('order_not_found', 404);
      return { order };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.get('/api/billing/subscriptions', auth, async (request, reply) => {
    try {
      const account = await activeAccount(pool, request.auth);
      return { subscriptions: await listSubscriptions(pool, account.id) };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.get('/api/billing/credits', auth, async (request, reply) => {
    try {
      if (!isUuid(request.query?.appId)) throw new BillingError('invalid_app', 400);
      const account = await activeAccount(pool, request.auth);
      return { balance: await creditBalance(pool, { accountId: account.id, appId: request.query.appId }) };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
  app.get('/api/billing/credits/ledger', auth, async (request, reply) => {
    try {
      const appId = request.query?.appId;
      if (appId && !isUuid(appId)) throw new BillingError('invalid_app', 400);
      const account = await activeAccount(pool, request.auth);
      return { entries: await listCreditLedger(pool, { accountId: account.id, appId: appId || null }) };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/api/billing/credits/holds', write, async (request, reply) => {
    try {
      const account = await activeAccount(pool, request.auth);
      const body = request.body ?? {};
      const result = await holdCredits(pool, {
        accountId: account.id,
        appId: body.appId,
        userId: request.auth.userId,
        requestId: body.requestId,
        authorizedUnits: body.authorizedUnits,
        pricingSnapshot: body.pricingSnapshot,
      });
      return writeResult(reply, result, result.duplicate ? 200 : 201);
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
  app.post('/api/billing/credits/holds/:holdId/settle', write, async (request, reply) => {
    try {
      if (!isUuid(request.params.holdId)) throw new BillingError('invalid_credit_hold', 400);
      const result = await settleCredits(pool, { holdId: request.params.holdId, settledUnits: request.body?.settledUnits });
      return writeResult(reply, result);
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
  app.post('/api/billing/credits/holds/:holdId/release', write, async (request, reply) => {
    try {
      if (!isUuid(request.params.holdId)) throw new BillingError('invalid_credit_hold', 400);
      return writeResult(reply, await releaseCredits(pool, request.params.holdId));
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.get('/api/accounts/:accountId/apps/:appId/seats', auth, async (request, reply) => {
    try {
      const { accountId, appId } = request.params;
      if (!isUuid(accountId) || !isUuid(appId)) throw new BillingError('invalid_seat_request', 400);
      await activeAccount(pool, request.auth, accountId);
      return { seats: await listEnterpriseSeats(pool, { accountId, appId, requestedBy: request.auth.userId }) };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
  app.post('/api/accounts/:accountId/apps/:appId/seats', write, async (request, reply) => {
    try {
      const { accountId, appId } = request.params;
      const body = request.body ?? {};
      const result = await assignEnterpriseSeat(pool, {
        accountId, appId, subscriptionId: body.subscriptionId, userId: body.userId, assignedBy: request.auth.userId,
      });
      return writeResult(reply, result, result.duplicate ? 200 : 201);
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
  app.delete('/api/accounts/:accountId/apps/:appId/seats/:userId', write, async (request, reply) => {
    try {
      const { accountId, appId, userId } = request.params;
      const result = await revokeEnterpriseSeat(pool, {
        accountId, appId, subscriptionId: request.body?.subscriptionId ?? request.query?.subscriptionId,
        userId, revokedBy: request.auth.userId,
      });
      return reply.code(result.removed ? 204 : 404).send(result.removed ? undefined : { error: 'seat_not_found' });
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/api/accounts/:accountId/invites', write, async (request, reply) => {
    try {
      const account = await activeAccount(pool, request.auth, request.params.accountId);
      const body = request.body ?? {};
      return writeResult(reply, await createEnterpriseInvite(pool, {
        accountId: account.id, createdBy: request.auth.userId, region,
        expiresAt: body.expiresAt, role: body.role,
      }), 201);
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
  app.post('/api/invites/:token/claim', write, async (request, reply) => {
    try {
      return writeResult(reply, await claimEnterpriseInvite(pool, {
        token: request.params.token, userId: request.auth.userId, region,
      }), 201);
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
  app.put('/api/accounts/:accountId/apps/:appId/members/:userId/limit', write, async (request, reply) => {
    try {
      const { accountId, appId, userId } = request.params;
      return { limit: await setMemberLimit(pool, {
        accountId, appId, userId, cycleLimitUnits: request.body?.cycleLimitUnits ?? null, updatedBy: request.auth.userId,
      }) };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/api/internal/billing/outbox/process', write, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send({ error: 'operator_required' });
    try {
      return processPendingOutbox(pool, { limit: request.body?.limit });
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.get('/api/admin/billing/reconciliation', auth, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send({ error: 'operator_required' });
    try {
      return runReconciliation(pool, { region, currency });
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/api/admin/billing/payments/:paymentOrderId/refund', write, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send({ error: 'operator_required' });
    try {
      const body = request.body ?? {};
      const result = await refundPaymentOrder(pool, {
        paymentOrderId: request.params.paymentOrderId,
        amountMinor: body.amountMinor,
        operatorUserId: request.auth.userId,
        idempotencyKey: body.idempotencyKey,
      });
      return {
        paymentOrder: serializePayment(result.payment),
        wallet: serializeWallet(result.wallet),
        duplicate: result.duplicate,
      };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/api/admin/billing/payments/:paymentOrderId/chargeback', write, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send({ error: 'operator_required' });
    try {
      const body = request.body ?? {};
      const result = await recordChargeback(pool, {
        paymentOrderId: request.params.paymentOrderId,
        amountMinor: body.amountMinor,
        disputeId: body.disputeId,
      });
      return {
        paymentOrder: serializePayment(result.payment),
        wallet: serializeWallet(result.wallet),
        duplicate: result.duplicate,
      };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
}
