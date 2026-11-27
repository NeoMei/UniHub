import { randomUUID } from 'node:crypto';
import { isUuid, transaction } from './database.js';

const accountPermissions = new Map([
  ['recharge', 'canRecharge'], ['purchase', 'canPurchase'], ['issue-key', 'canIssueKey'],
]);
const accountQuery = `
  SELECT a.id, a.kind, a.name, m.role,
    (a.kind = 'personal' OR COALESCE(m.can_recharge, false)) AS "canRecharge",
    (a.kind = 'personal' OR COALESCE(m.can_purchase AND (m.role IN ('owner', 'admin') OR m.has_seat), false)) AS "canPurchase",
    (a.kind = 'personal' OR COALESCE(m.has_seat, false)) AS "canIssueKey"
  FROM accounts a
  LEFT JOIN enterprise_members m ON a.id = m.account_id AND m.user_id = $1
  WHERE ((a.kind = 'personal' AND a.owner_id = $1) OR (a.kind = 'enterprise' AND m.user_id IS NOT NULL))`;

export async function accountContext(pool, session) {
  const { rows: accounts } = await pool.query(`${accountQuery} ORDER BY a.kind DESC, a.name, a.id`, [session.userId]);
  return {
    user: { id: session.userId, name: session.name, email: session.email },
    activeAccountId: session.activeAccountId,
    activeAccount: accounts.find(({ id }) => id === session.activeAccountId) ?? null,
    accounts,
    csrf: session.csrf,
  };
}

async function getAccount(pool, userId, accountId) {
  const { rows: [account] } = await pool.query(`${accountQuery} AND a.id = $2`, [userId, accountId]);
  return account ?? null;
}

async function createEnterprise(pool, userId, name) {
  const id = randomUUID();
  await transaction(pool, async (connection) => {
    await connection.query(`INSERT INTO accounts (id, kind, name, owner_id) VALUES ($1, 'enterprise', $2, $3)`, [id, name, userId]);
    await connection.query(`
      INSERT INTO enterprise_members (account_id, user_id, role, can_recharge, can_purchase)
      VALUES ($1, $2, 'owner', true, true)`, [id, userId]);
  });
  return id;
}

function mayManageMembers(account) {
  return account?.role === 'owner' || account?.role === 'admin';
}

function canChangeMember(actor, member, update) {
  if (member?.role === 'owner') {
    return actor.role === 'owner' && update?.role === 'owner' && update.canPurchase && update.canRecharge;
  }
  if (update?.role === 'owner') return false;
  if (actor.role === 'owner') return true;
  if (member?.role === 'admin' || update?.role === 'admin') return false;
  return !(update?.canPurchase && !actor.canPurchase) && !(update?.canRecharge && !actor.canRecharge);
}

async function changeMember(pool, session, accountId, userId, update) {
  return transaction(pool, async (connection) => {
    await connection.query(`SELECT id FROM accounts WHERE id = $1 AND kind = 'enterprise' FOR UPDATE`, [accountId]);
    const actor = await getAccount(connection, session.userId, accountId);
    if (!mayManageMembers(actor)) return 403;
    const member = await getAccount(connection, userId, accountId);
    if (!canChangeMember(actor, member, update)) return 403;
    if (update === null) {
      await connection.query('DELETE FROM enterprise_members WHERE account_id = $1 AND user_id = $2', [accountId, userId]);
      return 204;
    }
    const { rowCount } = await connection.query(`
      INSERT INTO enterprise_members (account_id, user_id, role, can_recharge, can_purchase, has_seat)
      SELECT $1, id, $3, $4, $5, $6 FROM users WHERE id = $2
      ON CONFLICT (account_id, user_id) DO UPDATE SET role = EXCLUDED.role,
        can_recharge = EXCLUDED.can_recharge, can_purchase = EXCLUDED.can_purchase, has_seat = EXCLUDED.has_seat`,
    [accountId, userId, update.role, update.canRecharge, update.canPurchase, update.hasSeat]);
    return rowCount === 1 ? 204 : 404;
  });
}

function formRequest(request) {
  return request.headers['content-type']?.startsWith('application/x-www-form-urlencoded');
}

function validMember(update) {
  return update && ['owner', 'admin', 'member'].includes(update.role)
    && ['canRecharge', 'canPurchase', 'hasSeat'].every((key) => typeof update[key] === 'boolean');
}

export function registerAccountRoutes(app, pool, auth, write, { isPlatformOperator = () => false, region, currency } = {}) {
  app.get('/api/session', auth, async (request) => {
    const context = await accountContext(pool, request.auth);
    const platformOp = isPlatformOperator(request.auth);
    const { rows: operatorApps } = await pool.query(`
      SELECT a.id, a.name
      FROM app_operators o
      JOIN apps a ON a.id = o.app_id
      WHERE o.user_id = $1
      ORDER BY a.name ASC`, [request.auth.userId]);
    return {
      ...context,
      region,
      currency,
      isPlatformOperator: platformOp,
      operatorApps,
    };
  });

  app.get('/api/apps/public', async () => {
    const { rows } = await pool.query(`
      SELECT id, name, description FROM apps
      WHERE enabled = true
      ORDER BY name ASC`);
    return { apps: rows, region, currency };
  });

  app.post('/api/session/account', write, async (request, reply) => {
    const id = request.body?.accountId;
    if (!isUuid(id)) return reply.code(400).send({ error: 'invalid_account' });
    const account = await getAccount(pool, request.auth.userId, id);
    if (!account) return reply.code(403).send({ error: 'account_forbidden' });
    await pool.query('UPDATE portal_sessions SET active_account_id = $1 WHERE id = $2', [id, request.auth.id]);
    if (formRequest(request)) return reply.redirect('/', 303);
    return { activeAccountId: account.id };
  });

  app.post('/api/accounts/enterprises', write, async (request, reply) => {
    const name = typeof request.body?.name === 'string' ? request.body.name.trim() : '';
    if (name.length < 1 || name.length > 160 || name.includes('\0')) return reply.code(400).send({ error: 'invalid_name' });
    const accountId = await createEnterprise(pool, request.auth.userId, name);
    if (formRequest(request)) return reply.redirect('/', 303);
    return reply.code(201).send({ accountId });
  });

  app.get('/api/active-account/permissions/:action', auth, async (request, reply) => {
    const permission = accountPermissions.get(request.params.action);
    if (!permission) return reply.code(404).send({ error: 'unknown_permission' });
    const account = await getAccount(pool, request.auth.userId, request.auth.activeAccountId);
    if (!account?.[permission]) return reply.code(403).send({ error: 'account_forbidden' });
    return { accountId: account.id, action: request.params.action };
  });

  app.get('/api/accounts/:accountId/members', auth, async (request, reply) => {
    const { accountId } = request.params;
    if (!isUuid(accountId)) return reply.code(400).send({ error: 'invalid_account' });
    if (!mayManageMembers(await getAccount(pool, request.auth.userId, accountId))) {
      return reply.code(403).send({ error: 'account_forbidden' });
    }
    const { rows: members } = await pool.query(`
      SELECT u.id, u.name, m.role, m.can_recharge AS "canRecharge", m.can_purchase AS "canPurchase", m.has_seat AS "hasSeat"
      FROM enterprise_members m JOIN users u ON m.user_id = u.id WHERE m.account_id = $1 ORDER BY u.name, u.id`, [accountId]);
    return { members };
  });

  const memberPath = '/api/accounts/:accountId/members/:userId';
  async function editMember(request, reply) {
    const { accountId, userId } = request.params;
    const update = request.method === 'DELETE' ? null : request.body;
    if (!isUuid(accountId) || !isUuid(userId) || (request.method === 'PUT' && !validMember(update))) {
      return reply.code(400).send({ error: 'invalid_member' });
    }
    const status = await changeMember(pool, request.auth, accountId, userId, update);
    if (status === 204) return reply.code(204).send();
    return reply.code(status).send({ error: status === 403 ? 'account_forbidden' : 'user_not_found' });
  }
  app.put(memberPath, write, editMember);
  app.delete(memberPath, write, editMember);
}
