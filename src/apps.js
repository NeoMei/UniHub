import { createHash, randomBytes, randomUUID, timingSafeEqual } from 'node:crypto';
import { isUuid, transaction } from './database.js';

const appFields = 'id, name, redirect_uris AS "redirectUris", enabled';
const appPath = '/api/admin/apps/:appId';
const appError = { error: 'invalid_app' };
const forbidden = { error: 'app_forbidden' };
const invalidLogin = { error: 'invalid_app_login' };
const loopback = new Set(['localhost', '127.0.0.1', '[::1]']);

function hash(value) {
  return createHash('sha256').update(value).digest();
}

function validRedirect(value) {
  if (typeof value !== 'string' || Buffer.byteLength(value) > 1024
    || /[\s\u0000-\u001f\u007f\\#]/u.test(value) || !/^https?:\/\//i.test(value)) return false;
  try {
    const url = new URL(value);
    return (url.protocol === 'https:' || (url.protocol === 'http:' && loopback.has(url.hostname)))
      && !url.username && !url.password && !url.searchParams.has('code') && !url.searchParams.has('state');
  } catch {
    return false;
  }
}

function readSettings(body, enabled = body?.enabled) {
  const name = typeof body?.name === 'string' ? body.name.trim() : '';
  const redirectUris = body?.redirectUris;
  if (!name || name.length > 160 || name.includes('\0') || !Array.isArray(redirectUris) || redirectUris.length < 1
    || redirectUris.length > 20 || !redirectUris.every(validRedirect) || typeof enabled !== 'boolean') return null;
  return { name, redirectUris: [...new Set(redirectUris)], enabled };
}

export async function readAppLogin(pool, { app_id: appId, redirect_uri: redirectUri, state }) {
  if (!isUuid(appId) || !validRedirect(redirectUri) || typeof state !== 'string' || !state || Buffer.byteLength(state) > 256) return null;
  const { rowCount } = await pool.query(
    'SELECT id FROM apps WHERE id = $1 AND enabled AND $2 = ANY(redirect_uris)', [appId, redirectUri],
  );
  return rowCount === 1 ? { appId, redirectUri, state } : null;
}

export async function issueAppLogin(pool, sessionId, { appId, redirectUri, state }) {
  const code = randomBytes(32).toString('base64url');
  const { rowCount } = await pool.query(`
    INSERT INTO app_login_codes (code_hash, app_id, session_id, redirect_uri, expires_at)
    SELECT $1, a.id, s.id, $4, statement_timestamp() + interval '2 minutes'
    FROM apps a CROSS JOIN portal_sessions s
    WHERE a.id = $2 AND a.enabled AND $4 = ANY(a.redirect_uris)
      AND s.id = $3 AND s.revoked_at IS NULL AND s.expires_at > statement_timestamp()
    FOR SHARE OF a, s`, [hash(code), appId, sessionId, redirectUri]);
  if (rowCount !== 1) return null;
  const target = new URL(redirectUri).href;
  const separator = target.includes('?') ? '&' : '?';
  return `${target}${separator}${new URLSearchParams({ code, state })}`;
}

function readCredentials(header) {
  if (typeof header !== 'string' || header.length > 256) return null;
  const match = /^Basic ([A-Za-z0-9+/]+=*)$/i.exec(header);
  if (!match) return null;
  const credentials = Buffer.from(match[1], 'base64').toString('utf8');
  const separator = credentials.indexOf(':');
  const id = credentials.slice(0, separator);
  if (separator < 0 || !isUuid(id)) return null;
  return { id, secretHash: hash(credentials.slice(separator + 1)) };
}

async function exchange(pool, credentials, body) {
  if (!credentials) return { status: 401 };
  return transaction(pool, async (connection) => {
    const { rows: [app] } = await connection.query(
      'SELECT enabled, client_secret_hash FROM apps WHERE id = $1 FOR SHARE', [credentials.id],
    );
    if (!app?.enabled || !timingSafeEqual(app.client_secret_hash, credentials.secretHash)) return { status: 401 };
    const { code, redirectUri } = body ?? {};
    if (typeof code !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(code) || !validRedirect(redirectUri)) return { status: 400 };

    const codeHash = hash(code);
    const { rows: [session] } = await connection.query(`
      SELECT s.id, i.user_id AS "userId"
      FROM app_login_codes c
      JOIN apps a ON a.id = c.app_id
      JOIN portal_sessions s ON s.id = c.session_id
      JOIN oidc_identities i USING (issuer, subject)
      WHERE c.code_hash = $1 AND c.app_id = $2 AND c.redirect_uri = $3 AND $3 = ANY(a.redirect_uris)
        AND s.revoked_at IS NULL AND s.expires_at > statement_timestamp()
      FOR SHARE OF s`, [codeHash, credentials.id, redirectUri]);
    if (!session) return { status: 400 };

    const { rowCount } = await connection.query(`
      UPDATE app_login_codes SET used_at = statement_timestamp()
      WHERE code_hash = $1 AND used_at IS NULL AND expires_at > statement_timestamp()`, [codeHash]);
    return rowCount === 1 ? { status: 200, userId: session.userId } : { status: 400 };
  });
}

export function registerAppRoutes(app, pool, auth, write, isPlatformOperator) {
  app.post('/api/admin/apps', { ...write, bodyLimit: 32_768 }, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send(forbidden);
    const settings = readSettings(request.body, request.body?.enabled === undefined ? true : request.body.enabled);
    if (!settings) return reply.code(400).send(appError);
    const clientSecret = randomBytes(32).toString('base64url');
    const { rows: [created] } = await pool.query(`
      INSERT INTO apps (id, name, client_secret_hash, redirect_uris, enabled)
      VALUES ($1, $2, $3, $4, $5) RETURNING ${appFields}`,
    [randomUUID(), settings.name, hash(clientSecret), settings.redirectUris, settings.enabled]);
    return reply.code(201).send({ app: created, clientSecret });
  });

  app.get(appPath, auth, async (request, reply) => {
    const { appId } = request.params;
    if (!isUuid(appId)) return reply.code(400).send(appError);
    const { rows: [registered] } = await pool.query(`
      SELECT ${appFields} FROM apps
      WHERE id = $1 AND ($2 OR EXISTS (SELECT 1 FROM app_operators WHERE app_id = $1 AND user_id = $3))`,
    [appId, isPlatformOperator(request.auth), request.auth.userId]);
    return registered ? { app: registered } : reply.code(403).send(forbidden);
  });

  app.put(appPath, { ...write, bodyLimit: 32_768 }, async (request, reply) => {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send(forbidden);
    const { appId } = request.params;
    const settings = readSettings(request.body);
    if (!isUuid(appId) || !settings) return reply.code(400).send(appError);
    const updated = await transaction(pool, async (connection) => {
      const { rows: [registered] } = await connection.query(`
        UPDATE apps SET name = $1, redirect_uris = $2, enabled = $3 WHERE id = $4 RETURNING ${appFields}`,
      [settings.name, settings.redirectUris, settings.enabled, appId]);
      if (!registered) return null;
      await connection.query(`
        DELETE FROM app_login_codes
        WHERE app_id = $1 AND used_at IS NULL AND (NOT $2 OR NOT redirect_uri = ANY($3::text[]))`,
      [appId, registered.enabled, registered.redirectUris]);
      return registered;
    });
    return updated ? { app: updated } : reply.code(404).send({ error: 'app_not_found' });
  });

  const operatorPath = `${appPath}/operators/:userId`;
  async function editOperator(request, reply) {
    if (!isPlatformOperator(request.auth)) return reply.code(403).send(forbidden);
    const { appId, userId } = request.params;
    if (!isUuid(appId) || !isUuid(userId)) return reply.code(400).send(appError);
    if (request.method === 'DELETE') {
      await pool.query('DELETE FROM app_operators WHERE app_id = $1 AND user_id = $2', [appId, userId]);
    } else {
      const { rowCount } = await pool.query(`
        INSERT INTO app_operators (app_id, user_id)
        SELECT a.id, u.id FROM apps a CROSS JOIN users u WHERE a.id = $1 AND u.id = $2
        ON CONFLICT (app_id, user_id) DO UPDATE SET user_id = EXCLUDED.user_id`, [appId, userId]);
      if (!rowCount) return reply.code(404).send({ error: 'app_or_user_not_found' });
    }
    return reply.code(204).send();
  }
  app.put(operatorPath, write, editOperator);
  app.delete(operatorPath, write, editOperator);

  app.post('/api/apps/login/exchange', async (request, reply) => {
    const result = await exchange(pool, readCredentials(request.headers.authorization), request.body);
    if (result.status === 401) reply.header('www-authenticate', 'Basic realm="UniHub apps"');
    if (result.status !== 200) return reply.code(result.status).send(invalidLogin);
    return { userId: result.userId };
  });
}
