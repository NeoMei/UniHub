import { createHash, randomBytes } from 'node:crypto';
import { isUuid, transaction } from '../database.js';
import { BillingError } from './errors.js';

function hashToken(token) {
  return createHash('sha256').update(token).digest();
}

async function managerAccount(connection, accountId, userId) {
  const { rows: [account] } = await connection.query(`
    SELECT a.id, a.kind, m.role, m.can_purchase, m.can_recharge
    FROM accounts a JOIN enterprise_members m ON m.account_id = a.id AND m.user_id = $2
    WHERE a.id = $1 AND a.kind = 'enterprise'`, [accountId, userId]);
  if (!account || !['owner', 'admin'].includes(account.role)) throw new BillingError('account_forbidden', 403);
  return account;
}

function mapSeat(row) {
  return {
    accountId: row.account_id,
    appId: row.app_id,
    subscriptionId: row.subscription_id,
    userId: row.user_id,
    assignedBy: row.assigned_by,
    assignedAt: row.assigned_at,
  };
}

export async function assignEnterpriseSeat(pool, { accountId, appId, subscriptionId, userId, assignedBy }) {
  if (![accountId, appId, subscriptionId, userId, assignedBy].every(isUuid)) {
    throw new BillingError('invalid_seat_request', 400);
  }
  return transaction(pool, async (connection) => {
    await managerAccount(connection, accountId, assignedBy);
    const { rows: [subscription] } = await connection.query(`
      SELECT id, app_id, account_id, seat_count FROM app_subscriptions
      WHERE id = $1 AND account_id = $2 AND app_id = $3 AND status = 'active'
      FOR UPDATE`, [subscriptionId, accountId, appId]);
    if (!subscription) throw new BillingError('subscription_not_found', 404);
    const { rowCount: memberExists } = await connection.query(
      'SELECT 1 FROM enterprise_members WHERE account_id = $1 AND user_id = $2', [accountId, userId],
    );
    if (!memberExists) throw new BillingError('member_not_found', 404);
    const { rows: [existing] } = await connection.query(`
      SELECT * FROM enterprise_app_seats
      WHERE account_id = $1 AND app_id = $2 AND subscription_id = $3 AND user_id = $4
      FOR UPDATE`, [accountId, appId, subscriptionId, userId]);
    if (existing) return { seat: mapSeat(existing), duplicate: true };
    const { rows: [count] } = await connection.query(
      'SELECT count(*)::int AS count FROM enterprise_app_seats WHERE subscription_id = $1', [subscriptionId],
    );
    if (count.count >= subscription.seat_count) throw new BillingError('seat_limit_reached', 409);
    const { rows: [created] } = await connection.query(`
      INSERT INTO enterprise_app_seats (account_id, app_id, subscription_id, user_id, assigned_by)
      VALUES ($1, $2, $3, $4, $5) RETURNING *`, [accountId, appId, subscriptionId, userId, assignedBy]);
    return { seat: mapSeat(created), duplicate: false };
  });
}

export async function revokeEnterpriseSeat(pool, { accountId, appId, subscriptionId, userId, revokedBy }) {
  if (![accountId, appId, subscriptionId, userId, revokedBy].every(isUuid)) {
    throw new BillingError('invalid_seat_request', 400);
  }
  return transaction(pool, async (connection) => {
    await managerAccount(connection, accountId, revokedBy);
    const { rowCount } = await connection.query(`
      DELETE FROM enterprise_app_seats
      WHERE account_id = $1 AND app_id = $2 AND subscription_id = $3 AND user_id = $4`,
    [accountId, appId, subscriptionId, userId]);
    return { removed: rowCount === 1 };
  });
}

export async function listEnterpriseSeats(pool, { accountId, appId, requestedBy }) {
  await transaction(pool, (connection) => managerAccount(connection, accountId, requestedBy));
  const { rows } = await pool.query(`
    SELECT account_id, app_id, subscription_id, user_id, assigned_by, assigned_at
    FROM enterprise_app_seats WHERE account_id = $1 AND app_id = $2
    ORDER BY assigned_at, user_id`, [accountId, appId]);
  return rows.map(mapSeat);
}

export async function createEnterpriseInvite(pool, {
  accountId, createdBy, region, expiresAt: rawExpiresAt, role = 'member',
}) {
  if (!isUuid(accountId) || !isUuid(createdBy) || !['admin', 'member'].includes(role)) {
    throw new BillingError('invalid_invite', 400);
  }
  const expiresAt = new Date(rawExpiresAt);
  if (Number.isNaN(expiresAt.getTime()) || expiresAt <= new Date() || expiresAt.getTime() > Date.now() + 30 * 86400_000) {
    throw new BillingError('invalid_invite_expiry', 400);
  }
  const token = randomBytes(32).toString('base64url');
  await transaction(pool, async (connection) => {
    await managerAccount(connection, accountId, createdBy);
    await connection.query(`
      INSERT INTO enterprise_invites (token_hash, account_id, region, role, created_by, expires_at)
      VALUES ($1, $2, $3, $4, $5, $6)`, [hashToken(token), accountId, region, role, createdBy, expiresAt]);
  });
  return { token, accountId, region, role, expiresAt };
}

export async function claimEnterpriseInvite(pool, { token, userId, region }) {
  if (typeof token !== 'string' || token.length < 32 || !isUuid(userId)) throw new BillingError('invalid_invite', 400);
  return transaction(pool, async (connection) => {
    const { rows: [invite] } = await connection.query(
      'SELECT * FROM enterprise_invites WHERE token_hash = $1 FOR UPDATE', [hashToken(token)],
    );
    if (!invite) throw new BillingError('invite_not_found', 404);
    if (invite.region !== region) throw new BillingError('invite_region_mismatch', 400);
    if (invite.claimed_by) throw new BillingError('invite_already_claimed', 409);
    if (new Date(invite.expires_at) <= new Date()) throw new BillingError('invite_expired', 409);
    await connection.query(`
      INSERT INTO enterprise_members (account_id, user_id, role, can_recharge, can_purchase, has_seat)
      VALUES ($1, $2, $3, false, false, false)
      ON CONFLICT (account_id, user_id) DO UPDATE SET role =
        CASE WHEN enterprise_members.role = 'owner' THEN enterprise_members.role ELSE EXCLUDED.role END`,
    [invite.account_id, userId, invite.role]);
    await connection.query(`
      UPDATE enterprise_invites SET claimed_by = $1, claimed_at = statement_timestamp()
      WHERE token_hash = $2`, [userId, invite.token_hash]);
    return { accountId: invite.account_id, role: invite.role, claimed: true };
  });
}

export async function setMemberLimit(pool, { accountId, appId, userId, cycleLimitUnits, updatedBy }) {
  if (![accountId, appId, userId, updatedBy].every(isUuid)) throw new BillingError('invalid_member_limit', 400);
  if (cycleLimitUnits !== null && (!/^(?:0|[1-9]\d*)$/.test(String(cycleLimitUnits)))) {
    throw new BillingError('invalid_member_limit', 400);
  }
  return transaction(pool, async (connection) => {
    await managerAccount(connection, accountId, updatedBy);
    const { rowCount } = await connection.query(`
      INSERT INTO enterprise_member_limits (account_id, app_id, user_id, cycle_limit_units)
      SELECT $1, $2, $3, $4
      WHERE EXISTS (SELECT 1 FROM enterprise_members WHERE account_id = $1 AND user_id = $3)
      ON CONFLICT (account_id, app_id, user_id)
      DO UPDATE SET cycle_limit_units = EXCLUDED.cycle_limit_units`, [accountId, appId, userId, cycleLimitUnits]);
    if (!rowCount) throw new BillingError('member_not_found', 404);
    return { accountId, appId, userId, cycleLimitUnits: cycleLimitUnits === null ? null : String(cycleLimitUnits) };
  });
}

export async function hasEnterpriseAppSeat(pool, { accountId, appId, userId }) {
  const { rowCount } = await pool.query(`
    SELECT 1 FROM enterprise_app_seats WHERE account_id = $1 AND app_id = $2 AND user_id = $3`,
  [accountId, appId, userId]);
  return rowCount === 1;
}
