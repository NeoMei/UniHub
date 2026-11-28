import { sendBillingError } from '../billing/errors.js';
import { reconcileExpiredHolds } from '../gateway/billing-v1.js';
import { isUuid } from '../database.js';
import { syncOrderToLago } from './lago.js';

const FORBIDDEN_SETTING_RE = /(secret|key|password|token|private|cert|logto_|unihub_region|database_url)/i;
const SETTING_KEY_RE = /^[a-zA-Z0-9][a-zA-Z0-9._-]{1,63}$/;

export function registerAdminIntegrationRoutes(app, pool, auth, write, isPlatformOperator, {
  region,
  currency,
  lagoConfig = {},
}) {
  app.get('/api/admin/settings', auth, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send({ error: 'operator_required' });
    const { rows } = await pool.query(
      'SELECT key, value_json AS value, updated_by AS "updatedBy", updated_at AS "updatedAt" FROM runtime_settings ORDER BY key ASC',
    );
    return { region, currency, settings: rows };
  });

  app.put('/api/admin/settings', { ...write, bodyLimit: 32_768 }, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send({ error: 'operator_required' });
    const body = request.body ?? {};
    const key = typeof body.key === 'string' ? body.key.trim() : '';
    if (!SETTING_KEY_RE.test(key) || FORBIDDEN_SETTING_RE.test(key)) {
      return reply.code(400).send({ error: 'forbidden_setting_key' });
    }
    if (body.value === undefined) return reply.code(400).send({ error: 'invalid_setting_value' });

    const { rows: [saved] } = await pool.query(`
      INSERT INTO runtime_settings (key, value_json, updated_by, updated_at)
      VALUES ($1, $2::jsonb, $3, statement_timestamp())
      ON CONFLICT (key) DO UPDATE SET
        value_json = EXCLUDED.value_json,
        updated_by = EXCLUDED.updated_by,
        updated_at = statement_timestamp()
      RETURNING key, value_json AS value, updated_by AS "updatedBy", updated_at AS "updatedAt"`, [
      key, JSON.stringify(body.value), request.auth.userId,
    ]);
    return { setting: saved };
  });

  app.get('/api/admin/overview', auth, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send({ error: 'operator_required' });
    const { rows: [stats] } = await pool.query(`
      SELECT
        (SELECT count(*)::int FROM apps) AS "appsCount",
        (SELECT count(*)::int FROM app_subscriptions WHERE status = 'active') AS "activeSubscriptionsCount",
        (SELECT count(*)::int FROM commerce_orders) AS "ordersCount",
        (SELECT count(*)::int FROM credit_holds WHERE status IN ('reserved', 'dispatching', 'in_flight', 'reconciling')) AS "openHoldsCount",
        (SELECT count(*)::int FROM platform_cost_differences) AS "platformDifferencesCount",
        (SELECT count(*)::int FROM commerce_orders WHERE lago_sync_status = 'failed') AS "failedLagoSyncsCount"`);
    return { region, currency, ...stats };
  });

  app.get('/api/admin/platform-differences', auth, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send({ error: 'operator_required' });
    const { rows } = await pool.query(`
      SELECT id, hold_id AS "holdId", account_id AS "accountId", app_id AS "appId", user_id AS "userId",
        region, currency, reason, expected_units::text AS "expectedUnits",
        settled_units::text AS "settledUnits", diff_units::text AS "diffUnits",
        details_json AS details, created_at AS "createdAt"
      FROM platform_cost_differences
      ORDER BY created_at DESC LIMIT 200`);
    return { differences: rows };
  });

  app.get('/api/admin/lago/failed', auth, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send({ error: 'operator_required' });
    const { rows } = await pool.query(`
      SELECT o.id, o.order_no AS "orderNo", o.account_id AS "accountId", o.card_tier_id AS "cardTierId",
        o.paid_minor::text AS "paidMinor", o.currency, o.created_at AS "createdAt",
        r.last_error AS "lastError", r.updated_at AS "lastAttemptAt"
      FROM commerce_orders o
      LEFT JOIN lago_sync_records r ON r.order_id = o.id
      WHERE o.lago_sync_status = 'failed'
      ORDER BY o.created_at DESC LIMIT 100`);
    return { orders: rows };
  });

  app.post('/api/admin/lago/retry/:orderId', write, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send({ error: 'operator_required' });
    const { orderId } = request.params;
    if (!isUuid(orderId)) return reply.code(400).send({ error: 'invalid_order' });
    try {
      return await syncOrderToLago(pool, orderId, lagoConfig);
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/api/internal/billing/holds/reconcile', write, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send({ error: 'operator_required' });
    try {
      const now = request.body?.now ? new Date(request.body.now) : new Date();
      return await reconcileExpiredHolds(pool, { now });
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
}
