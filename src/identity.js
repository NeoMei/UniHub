import { randomUUID } from 'node:crypto';
import { isUuid, transaction } from './database.js';

export async function createSession(pool, { issuer, subject, name, email, csrf, lifetime }) {
  return transaction(pool, async (connection) => {
    await connection.query('SELECT pg_advisory_xact_lock(hashtextextended($1, 0))', [JSON.stringify([issuer, subject])]);
    const { rows: [identity] } = await connection.query(
      'SELECT user_id FROM oidc_identities WHERE issuer = $1 AND subject = $2', [issuer, subject],
    );
    const userId = identity?.user_id ?? randomUUID();
    await connection.query(`
      INSERT INTO users (id, name, email) VALUES ($1, $2, $3)
      ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, email = EXCLUDED.email`, [userId, name, email]);
    if (!identity) {
      await connection.query('INSERT INTO oidc_identities (issuer, subject, user_id) VALUES ($1, $2, $3)', [issuer, subject, userId]);
      await connection.query(`
        INSERT INTO accounts (id, kind, name, owner_id) VALUES ($1, 'personal', '个人账户', $2)`, [randomUUID(), userId]);
    }
    const id = randomUUID();
    await connection.query(`
      INSERT INTO portal_sessions (id, issuer, subject, active_account_id, csrf, expires_at)
      SELECT $1, $2, $3, id, $4, now() + $5::integer * interval '1 second'
      FROM accounts WHERE kind = 'personal' AND owner_id = $6`, [id, issuer, subject, csrf, lifetime, userId]);
    return id;
  });
}

export async function readSession(pool, id) {
  if (!isUuid(id)) return null;
  const { rows: [session] } = await pool.query(`
    SELECT s.id, s.issuer, s.subject, s.csrf, s.active_account_id AS "activeAccountId",
      u.id AS "userId", u.name, u.email
    FROM portal_sessions s
    JOIN oidc_identities i USING (issuer, subject)
    JOIN users u ON u.id = i.user_id
    WHERE s.id = $1 AND s.revoked_at IS NULL AND s.expires_at > now()`, [id]);
  return session ?? null;
}

export async function revokeSession(pool, id) {
  await pool.query('UPDATE portal_sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL', [id]);
}
