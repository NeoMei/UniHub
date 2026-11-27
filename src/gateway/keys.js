import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { BillingError, sendBillingError } from '../billing/errors.js';
import { isUuid, transaction } from '../database.js';

function hashApiKey(secret) {
  return createHash('sha256').update(secret).digest();
}

function generateKeyMaterial() {
  const plaintext = `uh_live_${randomBytes(32).toString('base64url')}`;
  return {
    plaintext,
    prefix: plaintext.slice(0, 16),
    keyHash: hashApiKey(plaintext),
  };
}

function cleanName(value) {
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  if (!trimmed || trimmed.length > 80 || trimmed.includes('\0')) return null;
  return trimmed;
}

function mapKey(row) {
  return {
    id: row.id,
    accountId: row.account_id,
    appId: row.app_id,
    userId: row.user_id,
    region: row.region,
    currency: row.currency,
    name: row.name,
    keyPrefix: row.key_prefix,
    routerKeyId: row.router_key_id,
    status: row.status,
    lastUsedAt: row.last_used_at,
    createdAt: row.created_at,
    revokedAt: row.revoked_at,
  };
}

async function verifyKeySubjectAccess(connection, { accountId, appId, userId }) {
  const { rows: [account] } = await connection.query(`
    SELECT a.id, a.kind, a.owner_id, m.role
    FROM accounts a
    LEFT JOIN enterprise_members m ON m.account_id = a.id AND m.user_id = $2
    WHERE a.id = $1`, [accountId, userId]);
  if (!account) throw new BillingError('account_forbidden', 403);
  if (account.kind === 'personal') {
    if (account.owner_id !== userId) throw new BillingError('account_forbidden', 403);
    return { accountKind: 'personal' };
  }
  if (!account.role) throw new BillingError('account_forbidden', 403);

  // ALL enterprise members (including owner and admin) must hold an active per-app seat!
  const { rowCount } = await connection.query(`
    SELECT 1 FROM enterprise_app_seats e
    JOIN app_subscriptions s ON s.id = e.subscription_id AND s.status = 'active'
    WHERE e.account_id = $1 AND e.app_id = $2 AND e.user_id = $3`, [accountId, appId, userId]);
  if (rowCount === 0) throw new BillingError('app_seat_required', 403);
  return { accountKind: 'enterprise' };
}

export async function resolveModelKey(pool, rawKey, { region, currency }) {
  if (typeof rawKey !== 'string' || !rawKey.startsWith('uh_live_') || rawKey.length < 24) {
    throw new BillingError('invalid_api_key', 401);
  }
  const { rows: [keyRow] } = await pool.query(`
    SELECT k.*, a.kind AS account_kind, a.owner_id, m.role AS member_role
    FROM model_api_keys k
    JOIN accounts a ON a.id = k.account_id
    LEFT JOIN enterprise_members m ON m.account_id = k.account_id AND m.user_id = k.user_id
    WHERE k.key_hash = $1`, [hashApiKey(rawKey)]);
  if (!keyRow || keyRow.status !== 'active') {
    throw new BillingError('invalid_api_key', 401);
  }
  if (keyRow.region !== region || keyRow.currency !== currency) {
    throw new BillingError('region_currency_mismatch', 400);
  }
  if (keyRow.account_kind === 'personal') {
    if (keyRow.owner_id !== keyRow.user_id) throw new BillingError('account_forbidden', 403);
  } else {
    if (!keyRow.member_role) throw new BillingError('account_forbidden', 403);
    const { rowCount } = await pool.query(`
      SELECT 1 FROM enterprise_app_seats e
      JOIN app_subscriptions s ON s.id = e.subscription_id AND s.status = 'active'
      WHERE e.account_id = $1 AND e.app_id = $2 AND e.user_id = $3`,
    [keyRow.account_id, keyRow.app_id, keyRow.user_id]);
    if (rowCount === 0) throw new BillingError('app_seat_required', 403);
  }

  await pool.query('UPDATE model_api_keys SET last_used_at = statement_timestamp() WHERE id = $1', [keyRow.id]);
  return {
    id: keyRow.id,
    accountId: keyRow.account_id,
    accountKind: keyRow.account_kind,
    appId: keyRow.app_id,
    userId: keyRow.user_id,
    region: keyRow.region,
    currency: keyRow.currency,
    keyPrefix: keyRow.key_prefix,
  };
}

export function registerGatewayKeyRoutes(app, pool, auth, write, { region, currency }) {
  app.get('/api/keys', auth, async (request, reply) => {
    try {
      const accountId = request.auth.activeAccountId;
      const appId = request.query?.appId;
      if (appId && !isUuid(appId)) throw new BillingError('invalid_app', 400);
      const { rows } = await pool.query(`
        SELECT k.* FROM model_api_keys k
        WHERE k.account_id = $1 AND k.user_id = $2
          AND ($3::uuid IS NULL OR k.app_id = $3)
        ORDER BY k.created_at DESC LIMIT 100`, [accountId, request.auth.userId, appId ?? null]);
      return { keys: rows.map(mapKey) };
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/api/keys', write, async (request, reply) => {
    try {
      const body = request.body ?? {};
      const appId = body.appId;
      const name = cleanName(body.name ?? 'Default API Key');
      if (!isUuid(appId) || !name) throw new BillingError('invalid_key_request', 400);
      const accountId = request.auth.activeAccountId;
      const material = generateKeyMaterial();

      const created = await transaction(pool, async (connection) => {
        const { rowCount: appOk } = await connection.query(
          'SELECT 1 FROM apps WHERE id = $1 AND enabled', [appId],
        );
        if (!appOk) throw new BillingError('app_unavailable', 404);
        await verifyKeySubjectAccess(connection, { accountId, appId, userId: request.auth.userId });
        const id = randomUUID();
        const { rows: [row] } = await connection.query(`
          INSERT INTO model_api_keys (
            id, account_id, app_id, user_id, region, currency, name, key_prefix, key_hash, router_key_id
          ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
          RETURNING *`, [
          id, accountId, appId, request.auth.userId, region, currency, name,
          material.prefix, material.keyHash, `rk_${id.slice(0, 12)}`,
        ]);
        return row;
      });
      return reply.code(201).send({ key: mapKey(created), plaintextKey: material.plaintext });
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.post('/api/keys/:keyId/rotate', write, async (request, reply) => {
    try {
      const { keyId } = request.params;
      if (!isUuid(keyId)) throw new BillingError('invalid_key_request', 400);
      const accountId = request.auth.activeAccountId;
      const material = generateKeyMaterial();

      const created = await transaction(pool, async (connection) => {
        const { rows: [existing] } = await connection.query(`
          SELECT * FROM model_api_keys
          WHERE id = $1 AND account_id = $2 AND user_id = $3 FOR UPDATE`,
        [keyId, accountId, request.auth.userId]);
        if (!existing || existing.status !== 'active') throw new BillingError('key_not_found', 404);
        await verifyKeySubjectAccess(connection, { accountId, appId: existing.app_id, userId: request.auth.userId });

        await connection.query(
          "UPDATE model_api_keys SET status = 'revoked', revoked_at = statement_timestamp() WHERE id = $1",
          [existing.id],
        );
        const newId = randomUUID();
        const { rows: [row] } = await connection.query(`
          INSERT INTO model_api_keys (
            id, account_id, app_id, user_id, region, currency, name, key_prefix, key_hash, router_key_id
          ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
          RETURNING *`, [
          newId, existing.account_id, existing.app_id, existing.user_id, region, currency,
          existing.name, material.prefix, material.keyHash, `rk_${newId.slice(0, 12)}`,
        ]);
        return row;
      });
      return reply.code(201).send({ key: mapKey(created), plaintextKey: material.plaintext, rotatedFromId: keyId });
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });

  app.delete('/api/keys/:keyId', write, async (request, reply) => {
    try {
      const { keyId } = request.params;
      if (!isUuid(keyId)) throw new BillingError('invalid_key_request', 400);
      const { rowCount } = await pool.query(`
        UPDATE model_api_keys SET status = 'revoked', revoked_at = statement_timestamp()
        WHERE id = $1 AND account_id = $2 AND user_id = $3 AND status = 'active'`,
      [keyId, request.auth.activeAccountId, request.auth.userId]);
      if (rowCount === 0) return reply.code(404).send({ error: 'key_not_found' });
      return reply.code(204).send();
    } catch (error) {
      return sendBillingError(reply, error);
    }
  });
}
